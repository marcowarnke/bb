package cmd

import (
	"strings"
	"testing"
)

// Tests for the review and apply-feedback loops. Fixtures: see xDiff/docsDiff
// and the activities in cmd_test.go.

func TestDiffLineNumbersAndFilters(t *testing.T) {
	newFake(t)
	out, errOut, code := run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "-n", "--", "src/")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, want := range []string{numberedLegend, "=== src/x.go\n", "    1     1   CONTEXT 1/1", "    2       - REMOVED 2/2", "          3 + ADDED 3/3", "    4     5   CONTEXT 4/5"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "docs/a.md") {
		t.Error("path filter did not exclude docs/a.md")
	}
	out, _, _ = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "-n", "--exclude", "src/**")
	if strings.Contains(out, "src/x.go") || !strings.Contains(out, "=== docs/a.md (added)") {
		t.Fatalf("exclude failed:\n%s", out)
	}
	// Raw diff mode filters too.
	out, _, _ = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--", "nothing/")
	if out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
}

func TestDiffStatJSONAndMaxLines(t *testing.T) {
	newFake(t)
	out, _, _ := run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--stat")
	if !strings.Contains(out, "    +2     -1  src/x.go") || !strings.Contains(out, "2 files changed, +4 -1") {
		t.Fatalf("stat:\n%s", out)
	}
	out, _, _ = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--json")
	j := jq(t, out, `.files[0].hunks[0].lines[1] | "\(.type) \(.old) \(.new)"`)
	if j != "removed 2 null" {
		t.Fatalf("json line=%q", j)
	}
	out, errOut, _ := run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "-n", "--max-lines", "6")
	if strings.Count(out, "\n") > 7 || !strings.Contains(errOut, "src/x.go (partially shown)") || !strings.Contains(errOut, "docs/a.md") {
		t.Fatalf("max-lines out=%q err=%q", out, errOut)
	}
}

func TestDiffSinceLastReview(t *testing.T) {
	newFake(t)
	out, errOut, code := run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--since-last-review", "-n")
	if code != 0 || !strings.Contains(errOut, "Changes from old111 to abc123") || !strings.Contains(out, "=== src/x.go") || strings.Contains(out, "docs/a.md") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	_, errOut, code = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--since", "gone")
	if code == 0 || !strings.Contains(errOut, "rebased or force-pushed") {
		t.Fatalf("expected rebase hint, got %q", errOut)
	}
	_, errOut, _ = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--since", "abc123")
	if !strings.Contains(errOut, "No changes since") {
		t.Fatalf("err=%q", errOut)
	}
}

func TestCommentLineValidation(t *testing.T) {
	f := newFake(t)
	anchorOf := func(args ...string) (map[string]any, string) {
		t.Helper()
		_, errOut, code := run(t, "", append([]string{"pr", "comment", "7", "-R", "PROJ/repo", "-b", "x"}, args...)...)
		if code != 0 {
			return nil, errOut
		}
		return f.bodies["POST "+prBase+"/7/comments"]["anchor"].(map[string]any), ""
	}
	a, _ := anchorOf("--path", "src/x.go", "--line", "3")
	if a["lineType"] != "ADDED" || a["fileType"] != "TO" {
		t.Errorf("line 3 should infer ADDED/TO, got %v", a)
	}
	a, _ = anchorOf("--path", "src/x.go", "--line", "4")
	if a["lineType"] != "CONTEXT" {
		t.Errorf("line 4 should infer CONTEXT, got %v", a)
	}
	_, errOut := anchorOf("--path", "src/x.go", "--line", "9")
	if !strings.Contains(errOut, "new line 9 is not part of this diff") ||
		!strings.Contains(errOut, "commentable new-file lines (--line N): 1-5") ||
		!strings.Contains(errOut, "removed old-file lines (--line N --line-type removed): 2") {
		t.Errorf("bad error: %s", errOut)
	}
	_, errOut = anchorOf("--path", "src/x.go", "--line", "3", "--line-type", "removed")
	if !strings.Contains(errOut, "old line 3 is not a removed line") {
		t.Errorf("bad error: %s", errOut)
	}
	_, errOut = anchorOf("--path", "nope.go", "--line", "1")
	if !strings.Contains(errOut, "nope.go is not changed in this pull request") {
		t.Errorf("bad error: %s", errOut)
	}
	a, _ = anchorOf("--path", "docs/a.md")
	if a["path"] != "docs/a.md" || a["line"] != nil {
		t.Errorf("file-level anchor=%v", a)
	}
	_, errOut, code := run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "-b", "use this", "--path", "src/x.go", "--line", "3", "--suggestion", "\tbetter()")
	body := f.bodies["POST "+prBase+"/7/comments"]
	if code != 0 || body["text"] != "use this\n\n```suggestion\n\tbetter()\n```" {
		t.Fatalf("suggestion code=%d err=%q body=%v", code, errOut, body["text"])
	}
}

func TestReviewInput(t *testing.T) {
	f := newFake(t)
	bad := `{"event":"REQUEST_CHANGES","body":"Summary","comments":[
		{"path":"src/x.go","line":3,"body":"ok"},
		{"path":"src/x.go","line":42,"body":"bad line"},
		{"path":"nope.go","line":1,"body":"bad file"}]}`
	_, errOut, code := run(t, bad, "pr", "review", "7", "-R", "PROJ/repo", "--input", "-")
	if code == 0 || !strings.Contains(errOut, "2 of 3 comments are invalid") ||
		!strings.Contains(errOut, "comments[1] (src/x.go:42)") || !strings.Contains(errOut, "comments[2] (nope.go:1)") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if len(f.all["POST "+prBase+"/7/comments"]) != 0 || f.bodies["PUT "+prBase+"/7/participants/me"] != nil {
		t.Fatal("nothing may be posted when validation fails")
	}

	good := `{"event":"REQUEST_CHANGES","body":"Summary","comments":[
		{"path":"src/x.go","line":3,"body":"must fix","task":true},
		{"path":"src/x.go","line":2,"lineType":"removed","body":"why removed?"},
		{"body":"general note"}]}`
	out, errOut, code := run(t, good, "pr", "review", "7", "-R", "PROJ/repo", "--input", "-", "--dry-run")
	if code != 0 || out != "src/x.go:3\ttask\nsrc/x.go:2\tcomment\ngeneral\tcomment\n" || len(f.all["POST "+prBase+"/7/comments"]) != 0 {
		t.Fatalf("dry-run code=%d out=%q err=%q", code, out, errOut)
	}

	out, errOut, code = run(t, good, "pr", "review", "7", "-R", "PROJ/repo", "--input", "-")
	if code != 0 {
		t.Fatal(errOut)
	}
	posts := f.all["POST "+prBase+"/7/comments"]
	if len(posts) != 4 || posts[0]["severity"] != "BLOCKER" || posts[1]["anchor"].(map[string]any)["fileType"] != "FROM" ||
		posts[2]["anchor"] != nil || posts[3]["text"] != "Summary" {
		t.Fatalf("posts=%v", posts)
	}
	if f.bodies["PUT "+prBase+"/7/participants/me"]["status"] != "NEEDS_WORK" {
		t.Fatal("status not set")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "src/x.go:3\thttp") || !strings.HasPrefix(lines[3], "summary\thttp") {
		t.Fatalf("out=%q", out)
	}
	if !strings.Contains(errOut, "Requested changes to pull request PROJ/repo#7 at abc123 (3 comments, 1 tasks)") {
		t.Fatalf("err=%q", errOut)
	}

	// Flags override the file's event; unknown fields are rejected.
	run(t, `{"body":"lgtm"}`, "pr", "review", "7", "-R", "PROJ/repo", "--input", "-", "--approve")
	if f.bodies["PUT "+prBase+"/7/participants/me"]["status"] != "APPROVED" {
		t.Fatal("--approve should override")
	}
	_, errOut, code = run(t, `{"event":"APPROVE","coments":[]}`, "pr", "review", "7", "-R", "PROJ/repo", "--input", "-")
	if code == 0 || !strings.Contains(errOut, `unknown field "coments"`) {
		t.Fatalf("err=%q", errOut)
	}
}

func TestThreads(t *testing.T) {
	newFake(t)
	ids := func(args ...string) string {
		t.Helper()
		out, errOut, code := run(t, "", append([]string{"pr", "threads", "7", "-R", "PROJ/repo", "--json", "id", "--jq", "[.[].id] | map(tostring) | join(\",\")"}, args...)...)
		if code != 0 {
			t.Fatal(errOut)
		}
		return strings.TrimSpace(out)
	}
	// Sorted by file/line: task #12 at src/x.go:2, thread #10 at src/x.go:5; resolved #13 hidden.
	if got := ids(); got != "12,10" {
		t.Errorf("default=%s", got)
	}
	if got := ids("--all"); got != "12,10,13" {
		t.Errorf("--all=%s", got)
	}
	if got := ids("--needs-reply"); got != "10" {
		t.Errorf("--needs-reply=%s", got)
	}
	if got := ids("--started-by", "@me"); got != "12" {
		t.Errorf("--started-by=%s", got)
	}

	out, _, _ := run(t, "", "pr", "threads", "7", "-R", "PROJ/repo", "--json", "id,kind,lastCommentBy,needsReply,context,replies")
	if j := jq(t, out, `.[1] | "\(.kind) \(.lastCommentBy) \(.needsReply) \(.replies[0].id)"`); j != "thread alice true 11" {
		t.Errorf("thread json=%q", j)
	}
	if j := jq(t, out, `"[" + (.[0].context | join("|")) + "]"`); j != "[     1 │ package x|>    2 │ new()|     3 │ extra()|     4 │ tail|     5 │ end]" {
		t.Errorf("context=%q", j)
	}

	out, _, _ = run(t, "", "pr", "threads", "7", "-R", "PROJ/repo")
	for _, want := range []string{"#12 · TASK · open · src/x.go:2 (added) · by me", "#10 · THREAD · open · src/x.go:5 (added) · by bob · needs reply", "  ↳ #11 alice: reply", ">    5 │ end", "2 item(s) on #7, 1 need a reply"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestReplyAndResolve(t *testing.T) {
	f := newFake(t)
	// Replying to #11 resolves its thread root #10.
	out, errOut, code := run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "11", "-b", "Fixed in abc123", "--resolve")
	if code != 0 || !strings.Contains(out, "commentId=") || !strings.Contains(errOut, "Resolved thread 10") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if p := f.bodies["POST "+prBase+"/7/comments"]; p["parent"].(map[string]any)["id"] != float64(11) {
		t.Fatalf("reply payload=%v", p)
	}
	if p := f.bodies["PUT "+prBase+"/7/comments/10"]; p["threadResolved"] != true || p["version"] != float64(4) {
		t.Fatalf("resolve payload=%v", p)
	}
	// Tasks resolve by state; the body is optional.
	posts := len(f.all["POST "+prBase+"/7/comments"])
	_, errOut, code = run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "12", "--resolve")
	if code != 0 || f.bodies["PUT "+prBase+"/7/comments/12"]["state"] != "RESOLVED" || len(f.all["POST "+prBase+"/7/comments"]) != posts {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "12", "--reopen")
	if f.bodies["PUT "+prBase+"/7/comments/12"]["state"] != "OPEN" {
		t.Fatal("reopen failed")
	}
	_, errOut, code = run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "--resolve")
	if code == 0 || !strings.Contains(errOut, "--reply-to") {
		t.Fatalf("err=%q", errOut)
	}
}

func TestChecksWatchAndViewFields(t *testing.T) {
	newFake(t)
	out, errOut, code := run(t, "", "pr", "checks", "7", "-R", "PROJ/repo", "--watch")
	if code != 0 || !strings.Contains(errOut, "waiting: 1 pending (Lint)") || !strings.Contains(out, "Lint\tpass") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	out, _, _ = run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--json", "commits,unresolvedThreads")
	if j := jq(t, out, `"\(.unresolvedThreads) \(.commits[0].messageHeadline)"`); j != "2 Add feature" {
		t.Fatalf("view json=%q", j)
	}
}

func TestPathFilter(t *testing.T) {
	cases := []struct {
		pattern string
		match   []string
		noMatch []string
	}{
		{"src/", []string{"src/a.go", "src/x/y.go"}, []string{"srcx/a.go", "lib/src/a.go"}},
		{"src/api", []string{"src/api/a.go", "src/api"}, []string{"src/apiv2/a.go"}},
		{"*.lock", []string{"go.lock", "web/yarn.lock"}, []string{"lock.go"}},
		{"vendor/**", []string{"vendor/a/b.go"}, []string{"src/vendor/a.go"}},
		{"**/*_test.go", []string{"a_test.go", "pkg/a_test.go"}, []string{"pkg/a.go"}},
		{"cmd/*.go", []string{"cmd/main.go"}, []string{"cmd/sub/main.go"}},
	}
	for _, c := range cases {
		re := globRegexp(c.pattern)
		for _, p := range c.match {
			if !re.MatchString(p) {
				t.Errorf("%q should match %q", c.pattern, p)
			}
		}
		for _, p := range c.noMatch {
			if re.MatchString(p) {
				t.Errorf("%q should not match %q", c.pattern, p)
			}
		}
	}
}

func jq(t *testing.T, input, expr string) string {
	t.Helper()
	var b strings.Builder
	if err := runJQ(&b, expr, []byte(input)); err != nil {
		t.Fatalf("jq %s: %v\ninput: %s", expr, err, input)
	}
	return strings.TrimSpace(b.String())
}

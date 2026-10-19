package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

// fakeBB is a tiny in-memory Bitbucket Data Center.
type fakeBB struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	bodies   map[string]map[string]any   // last body per "METHOD path"
	all      map[string][]map[string]any // every body per "METHOD path"
	nextID   int
	srv      *httptest.Server
	merge    map[string]any // GET …/merge response; nil = mergeable
}

// Fixture diff for src/x.go (old → new):
//
//	old 1 package x   new 1 package x
//	old 2 old()       (removed)
//	                  new 2 new()    (added)
//	                  new 3 extra()  (added)
//	old 3 tail        new 4 tail
//	old 4 end         new 5 end
func xDiff() map[string]any {
	seg := func(typ string, lines ...[2]int) map[string]any {
		var ls []any
		for _, l := range lines {
			ls = append(ls, map[string]any{"source": l[0], "destination": l[1], "line": fmt.Sprintf("%s %d/%d", typ, l[0], l[1])})
		}
		return map[string]any{"type": typ, "lines": ls}
	}
	return map[string]any{
		"source": map[string]any{"toString": "src/x.go"}, "destination": map[string]any{"toString": "src/x.go"},
		"hunks": []any{map[string]any{
			"sourceLine": 1, "sourceSpan": 4, "destinationLine": 1, "destinationSpan": 5,
			"segments": []any{
				seg("CONTEXT", [2]int{1, 1}), seg("REMOVED", [2]int{2, 2}),
				seg("ADDED", [2]int{3, 2}, [2]int{3, 3}), seg("CONTEXT", [2]int{3, 4}, [2]int{4, 5}),
			},
		}},
	}
}

func docsDiff() map[string]any {
	return map[string]any{
		"destination": map[string]any{"toString": "docs/a.md"},
		"hunks": []any{map[string]any{"sourceLine": 0, "sourceSpan": 0, "destinationLine": 1, "destinationSpan": 2,
			"segments": []any{map[string]any{"type": "ADDED", "lines": []any{
				map[string]any{"source": 0, "destination": 1, "line": "# Doc"},
				map[string]any{"source": 0, "destination": 2, "line": "text"},
			}}}}},
	}
}

const xContent = "package x\nnew()\nextra()\ntail\nend\n"

const prBase = "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests"

func prJSONFixture(url string) map[string]any {
	repo := map[string]any{"id": 1, "slug": "repo", "project": map[string]any{"key": "PROJ"}}
	return map[string]any{
		"id": 7, "version": 3, "title": "Add feature", "description": "Body text", "state": "OPEN", "open": true,
		"createdDate": 1700000000000, "updatedDate": 1700000100000,
		"fromRef": map[string]any{"id": "refs/heads/feat", "displayId": "feat", "latestCommit": "abc123", "repository": repo},
		"toRef":   map[string]any{"id": "refs/heads/main", "displayId": "main", "latestCommit": "def456", "repository": repo},
		"author":  map[string]any{"user": map[string]any{"name": "alice", "slug": "alice"}},
		"reviewers": []any{
			map[string]any{"user": map[string]any{"name": "bob", "slug": "bob"}, "status": "APPROVED"},
			map[string]any{"user": map[string]any{"name": "carol", "slug": "carol"}, "status": "UNAPPROVED"},
		},
		"participants": []any{
			map[string]any{"user": map[string]any{"name": "me", "slug": "me"}, "role": "PARTICIPANT", "lastReviewedCommit": "old111"},
		},
		"links":      map[string]any{"self": []any{map[string]any{"href": url + "/projects/PROJ/repos/repo/pull-requests/7"}}},
		"properties": map[string]any{"commentCount": 2, "openTaskCount": 1},
	}
}

func page(vals ...any) map[string]any {
	if vals == nil {
		vals = []any{}
	}
	return map[string]any{"values": vals, "isLastPage": true}
}

func newFake(t *testing.T) *fakeBB {
	keyring.MockInit() // never touch the real OS keyring from tests
	f := &fakeBB{t: t, bodies: map[string]map[string]any{}, all: map[string][]map[string]any{}, nextID: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	t.Setenv("BB_CONFIG_DIR", t.TempDir())
	t.Setenv("BB_HOST", f.srv.URL)
	t.Setenv("BB_TOKEN", "secret")
	t.Setenv("BB_REPO", "")
	return f
}

func (f *fakeBB) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer secret" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"errors":[{"message":"Authentication failed"}]}`)
		return
	}
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, key+"?"+r.URL.RawQuery)
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			f.bodies[key] = m
			f.all[key] = append(f.all[key], m)
		}
	}
	f.nextID++
	id := f.nextID
	f.mu.Unlock()
	q := r.URL.Query()
	pr := prJSONFixture(f.srv.URL)
	var out any
	switch key {
	case "GET /plugins/servlet/applinks/whoami":
		fmt.Fprint(w, "me")
		return
	case "GET /rest/api/1.0/users/me":
		out = map[string]any{"name": "me", "slug": "me", "displayName": "Me"}
	case "GET /rest/api/1.0/users/bob":
		out = map[string]any{"name": "bob", "slug": "bob"}
	case "GET /rest/api/1.0/users/dave":
		w.WriteHeader(404)
		return
	case "GET /rest/api/1.0/users":
		out = page(map[string]any{"name": "dave.smith", "slug": "dave.smith", "displayName": "Dave Smith"})
	case "GET " + prBase + "/7":
		out = pr
	case "GET " + prBase:
		out = page(pr)
	case "GET " + prBase + "/7.diff":
		fmt.Fprint(w, "diff --git a/x b/x\n+added\n")
		return
	case "GET " + prBase + "/7/changes":
		out = page(map[string]any{"type": "MODIFY", "path": map[string]any{"toString": "src/x.go"}})
	case "GET " + prBase + "/7/activities":
		// Threads: #10 by bob on src/x.go:5 with alice's reply #11 (needs reply from me);
		// task #12 by me on src/x.go:2, open; #13 by carol, resolved.
		reply := map[string]any{"id": 11, "text": "reply", "author": map[string]any{"slug": "alice"}, "createdDate": 3}
		root := map[string]any{"id": 10, "text": "root comment", "author": map[string]any{"slug": "bob"}, "createdDate": 1, "comments": []any{reply}}
		task := map[string]any{"id": 12, "text": "handle nil", "author": map[string]any{"slug": "me"}, "createdDate": 4, "severity": "BLOCKER", "state": "OPEN"}
		done := map[string]any{"id": 13, "text": "nice", "author": map[string]any{"slug": "carol"}, "createdDate": 5, "threadResolved": true}
		out = page(
			map[string]any{"id": 5, "action": "COMMENTED", "comment": done},
			map[string]any{"id": 4, "action": "COMMENTED", "comment": task, "commentAnchor": map[string]any{"path": "src/x.go", "line": 2, "lineType": "ADDED", "fileType": "TO", "toHash": "abc123"}},
			map[string]any{"id": 2, "action": "COMMENTED", "comment": reply},
			map[string]any{"id": 1, "action": "COMMENTED", "comment": root, "commentAnchor": map[string]any{"path": "src/x.go", "line": 5, "lineType": "ADDED", "fileType": "TO", "toHash": "abc123"}},
			map[string]any{"id": 0, "action": "OPENED"},
		)
	case "POST " + prBase + "/7/comments":
		out = map[string]any{"id": id}
	case "GET " + prBase + "/7/comments/10", "GET " + prBase + "/7/comments/12":
		out = map[string]any{"id": 0, "version": 4}
	case "PUT " + prBase + "/7/comments/10", "PUT " + prBase + "/7/comments/12":
		out = map[string]any{}
	case "GET " + prBase + "/7/diff":
		out = map[string]any{"fromHash": "def456", "toHash": "abc123", "diffs": []any{xDiff(), docsDiff()}}
	case "GET " + prBase + "/7/diff/src/x.go":
		out = map[string]any{"diffs": []any{xDiff()}}
	case "GET " + prBase + "/7/diff/docs/a.md":
		out = map[string]any{"diffs": []any{docsDiff()}}
	case "GET /rest/api/1.0/projects/PROJ/repos/repo/compare/diff":
		if q.Get("from") != "abc123" || q.Get("to") != "old111" {
			w.WriteHeader(404)
			return
		}
		out = map[string]any{"diffs": []any{xDiff()}}
	case "GET /rest/api/1.0/projects/PROJ/repos/repo/raw/src/x.go":
		if q.Get("at") != "abc123" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, xContent)
		return
	case "GET " + prBase + "/7/commits":
		out = page(map[string]any{"id": "abc123", "message": "Add feature\n\nDetails", "author": map[string]any{"name": "Alice"}})
	case "PUT " + prBase + "/7/participants/me":
		out = map[string]any{}
	case "GET " + prBase + "/7/merge":
		out = map[string]any{"canMerge": true}
		if f.merge != nil {
			out = f.merge
		}
	case "POST " + prBase + "/7/merge", "POST " + prBase + "/7/decline", "PUT " + prBase + "/7":
		out = pr
	case "GET /rest/api/1.0/projects/PROJ/repos/repo/default-branch":
		out = map[string]any{"displayId": "main"}
	case "GET /rest/api/1.0/projects/PROJ/repos/repo/branches":
		out = page(map[string]any{"displayId": "feat2"})
	case "GET /rest/api/1.0/projects/PROJ/repos/repo":
		out = map[string]any{"id": 1, "slug": "repo", "project": map[string]any{"key": "PROJ"}}
	case "GET /rest/default-reviewers/1.0/projects/PROJ/repos/repo/reviewers":
		out = []any{map[string]any{"name": "carol"}, map[string]any{"name": "me"}}
	case "POST " + prBase:
		out = pr
	case "GET /rest/build-status/1.0/commits/abc123":
		lint := "INPROGRESS" // first poll pending, then green
		if strings.Count(strings.Join(f.requests, "\n"), "build-status") > 1 {
			lint = "SUCCESSFUL"
		}
		out = page(map[string]any{"state": "SUCCESSFUL", "name": "CI", "url": "http://ci"}, map[string]any{"state": lint, "name": "Lint"})
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"errors":[{"message":"no route %s"}]}`, key)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func run(t *testing.T, in string, args ...string) (string, string, int) {
	t.Helper()
	var o, e bytes.Buffer
	stdout, stderr, stdin = &o, &e, strings.NewReader(in)
	isTTY = func() bool { return false }
	root := NewRoot()
	root.SetArgs(args)
	root.SetOut(&o)
	root.SetErr(&e)
	sleep = func(time.Duration) {}
	code := 0
	if err := root.Execute(); err != nil {
		if ee, ok := err.(*ExitError); ok {
			code = ee.Code
			if ee.Err != nil {
				e.WriteString(ee.Err.Error())
			}
		} else {
			code = 1
			e.WriteString(err.Error())
		}
	}
	return o.String(), e.String(), code
}

func TestPRListAndJSON(t *testing.T) {
	newFake(t)
	out, errOut, code := run(t, "", "pr", "list", "-R", "PROJ/repo")
	if code != 0 || !strings.HasPrefix(out, "7\tAdd feature\tfeat\tOPEN\t") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	out, _, _ = run(t, "", "pr", "list", "-R", "PROJ/repo", "--json", "number,reviewDecision", "--jq", ".[0].reviewDecision")
	if strings.TrimSpace(out) != "APPROVED" {
		t.Fatalf("jq out=%q", out)
	}
	_, errOut, code = run(t, "", "pr", "list", "-R", "PROJ/repo", "--json", "nope")
	if code == 0 || !strings.Contains(errOut, "Unknown JSON field") {
		t.Fatalf("expected unknown field error, got %q", errOut)
	}
}

func TestPRViewComments(t *testing.T) {
	newFake(t)
	out, errOut, code := run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--comments")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, want := range []string{"title:\tAdd feature", "reviewers:\tbob (Approved), carol (Requested)", "id:10", "src/x.go:5 (ADDED)", "  ↳ alice", "reply"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The reply must only appear nested, not as its own root.
	if strings.Count(out, "id:11") != 1 {
		t.Errorf("reply duplicated:\n%s", out)
	}
	out, _, _ = run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--json", "comments,files", "--jq", ".comments[0].replies[0].body, .files[0].path")
	if out != "reply\nsrc/x.go\n" {
		t.Fatalf("json out=%q", out)
	}
}

func TestPRViewByURL(t *testing.T) {
	f := newFake(t)
	out, errOut, code := run(t, "", "pr", "view", f.srv.URL+"/projects/proj/repos/repo/pull-requests/7/overview", "-R", "OTHER/x", "--json", "number", "--jq", ".number")
	if code != 0 || strings.TrimSpace(out) != "7" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestPRDiff(t *testing.T) {
	newFake(t)
	out, _, _ := run(t, "", "pr", "diff", "7", "-R", "PROJ/repo")
	if out != "diff --git a/x b/x\n+added\n" {
		t.Fatalf("diff=%q", out)
	}
	out, _, _ = run(t, "", "pr", "diff", "7", "-R", "PROJ/repo", "--name-only")
	if out != "src/x.go\n" {
		t.Fatalf("names=%q", out)
	}
}

func TestPRInlineComment(t *testing.T) {
	f := newFake(t)
	out, errOut, code := run(t, "", "pr", "comment", "7", "-R", "PROJ/repo", "-b", "nit", "--path", "src/x.go", "--line", "2", "--line-type", "removed", "--task")
	if code != 0 || !strings.Contains(out, "/pull-requests/7/overview?commentId=") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	body := f.bodies["POST "+prBase+"/7/comments"]
	anchor := body["anchor"].(map[string]any)
	if anchor["fileType"] != "FROM" || anchor["lineType"] != "REMOVED" || anchor["line"] != float64(2) || body["severity"] != "BLOCKER" {
		t.Fatalf("payload=%v", body)
	}
	run(t, "body from stdin", "pr", "comment", "7", "-R", "PROJ/repo", "-F", "-", "--reply-to", "10")
	body = f.bodies["POST "+prBase+"/7/comments"]
	if body["text"] != "body from stdin" || body["parent"].(map[string]any)["id"] != float64(10) {
		t.Fatalf("reply payload=%v", body)
	}
}

func TestPRReview(t *testing.T) {
	f := newFake(t)
	_, errOut, code := run(t, "", "pr", "review", "7", "-R", "PROJ/repo", "--request-changes", "-b", "please fix")
	if code != 0 {
		t.Fatal(errOut)
	}
	if f.bodies["PUT "+prBase+"/7/participants/me"]["status"] != "NEEDS_WORK" {
		t.Fatalf("payload=%v", f.bodies)
	}
	if f.bodies["POST "+prBase+"/7/comments"]["text"] != "please fix" {
		t.Fatal("review body not posted")
	}
	_, _, code = run(t, "", "pr", "review", "7", "-R", "PROJ/repo", "--approve", "--comment")
	if code == 0 {
		t.Fatal("expected error for conflicting flags")
	}
}

func TestPRCreate(t *testing.T) {
	f := newFake(t)
	out, errOut, code := run(t, "", "pr", "create", "-R", "PROJ/repo", "--head", "feat2", "-t", "T", "-b", "B", "-r", "bob,dave", "--draft")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "/pull-requests/7") {
		t.Fatalf("out=%q", out)
	}
	body := f.bodies["POST "+prBase]
	var names []string
	for _, r := range body["reviewers"].([]any) {
		names = append(names, r.(map[string]any)["user"].(map[string]any)["name"].(string))
	}
	// bob (direct), dave.smith (fuzzy match), carol (default reviewer); "me" excluded as author.
	if strings.Join(names, ",") != "bob,dave.smith,carol" {
		t.Fatalf("reviewers=%v", names)
	}
	if body["toRef"].(map[string]any)["id"] != "refs/heads/main" || body["draft"] != true {
		t.Fatalf("payload=%v", body)
	}
	_, errOut, code = run(t, "", "pr", "create", "-R", "PROJ/repo", "--head", "missing", "-t", "T")
	if code == 0 || !strings.Contains(errOut, "push it first") {
		t.Fatalf("expected unpushed branch error, got %q", errOut)
	}
	_, errOut, code = run(t, "", "pr", "create", "-R", "PROJ/repo", "--head", "feat2")
	if code == 0 || !strings.Contains(errOut, "--title") {
		t.Fatalf("expected title error, got %q", errOut)
	}
}

func TestPREditReviewers(t *testing.T) {
	f := newFake(t)
	_, errOut, code := run(t, "", "pr", "edit", "7", "-R", "PROJ/repo", "--add-reviewer", "dave", "--remove-reviewer", "bob")
	if code != 0 {
		t.Fatal(errOut)
	}
	body := f.bodies["PUT "+prBase+"/7"]
	b, _ := json.Marshal(body["reviewers"])
	if string(b) != `[{"user":{"name":"carol"}},{"user":{"name":"dave.smith"}}]` || body["version"] != float64(3) {
		t.Fatalf("payload=%v", body)
	}
}

func TestPRMergeAndChecks(t *testing.T) {
	f := newFake(t)
	_, errOut, code := run(t, "", "pr", "merge", "7", "-R", "PROJ/repo", "--squash")
	if code != 0 || f.bodies["POST "+prBase+"/7/merge"]["strategyId"] != "squash" {
		t.Fatalf("code=%d err=%q bodies=%v", code, errOut, f.bodies)
	}
	out, _, code := run(t, "", "pr", "checks", "7", "-R", "PROJ/repo")
	if code != 8 || !strings.Contains(out, "CI\tpass\thttp://ci") || !strings.Contains(out, "Lint\tpending") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestAPICommand(t *testing.T) {
	f := newFake(t)
	out, errOut, code := run(t, "", "api", "projects/{project}/repos/{repo}/pull-requests", "-R", "PROJ/repo", "--paginate", "--jq", ".[].title")
	if code != 0 || out != "Add feature\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	run(t, "", "api", "-R", "PROJ/repo", "projects/{project}/repos/{repo}/pull-requests/7/comments", "-f", "text=hi", "-F", "parent[id]=10")
	body := f.bodies["POST "+prBase+"/7/comments"]
	if body["text"] != "hi" || body["parent"].(map[string]any)["id"] != float64(10) {
		t.Fatalf("payload=%v", body)
	}
}

func TestAuthFailureExitCode(t *testing.T) {
	newFake(t)
	t.Setenv("BB_TOKEN", "wrong")
	_, errOut, _ := run(t, "", "pr", "view", "7", "-R", "PROJ/repo")
	if !strings.Contains(errOut, "HTTP 401") {
		t.Fatalf("err=%q", errOut)
	}
}

// Every command must build its flag set without panicking (e.g. -h clashes).
func TestAllCommandsHelp(t *testing.T) {
	n := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			path := strings.Fields(sub.CommandPath())[1:]
			if _, errOut, code := run(t, "", append(path, "--help")...); code != 0 {
				t.Errorf("%s --help: code=%d err=%q", sub.CommandPath(), code, errOut)
			}
			n++
			walk(sub)
		}
	}
	walk(NewRoot())
	if n < 20 {
		t.Fatalf("only %d commands walked", n)
	}
}

func TestMergeBlockers(t *testing.T) {
	f := newFake(t)
	out, _, _ := run(t, "", "pr", "view", "7", "-R", "PROJ/repo")
	if !strings.Contains(out, "merge:\tready\n") {
		t.Fatalf("out=%q", out)
	}
	f.merge = map[string]any{"canMerge": false, "conflicted": true, "vetoes": []any{
		map[string]any{"summaryMessage": "Not all required reviewers have approved yet.", "detailedMessage": "At least 2 approvals are required."},
		map[string]any{"summaryMessage": "Open tasks", "detailedMessage": "Open tasks"},
	}}
	out, _, _ = run(t, "", "pr", "view", "7", "-R", "PROJ/repo")
	want := "merge:\tblocked: the pull request has merge conflicts; Not all required reviewers have approved yet: At least 2 approvals are required.; Open tasks\n"
	if !strings.Contains(out, want) {
		t.Fatalf("out=%q", out)
	}
	out, _, _ = run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--json", "mergeStateStatus,mergeBlockers", "--jq", ".mergeStateStatus, (.mergeBlockers | length)")
	if out != "DIRTY\n3\n" {
		t.Fatalf("json=%q", out)
	}
	_, errOut, code := run(t, "", "pr", "merge", "7", "-R", "PROJ/repo", "--dry-run")
	if code != 1 || !strings.Contains(errOut, "  - Open tasks") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	f.merge = nil
	_, errOut, code = run(t, "", "pr", "merge", "7", "-R", "PROJ/repo", "--dry-run", "--squash")
	if code != 0 || !strings.Contains(errOut, "can be merged (squash)") || f.bodies["POST "+prBase+"/7/merge"] != nil {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestBrowse(t *testing.T) {
	f := newFake(t)
	repo := f.srv.URL + "/projects/PROJ/repos/repo"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, repo + "/browse"},
		{[]string{"42"}, repo + "/pull-requests/42"},
		{[]string{"src/main.go:42", "-b", "release/1.0"}, repo + "/browse/src/main.go?at=refs%2Fheads%2Frelease%2F1.0#42"},
		{[]string{"./docs/a b.md:3-7"}, repo + "/browse/docs/a%20b.md#3-7"},
		{[]string{"abc1234"}, repo + "/commits/abc1234"},
		{[]string{"--commit=abc1234", "src"}, repo + "/browse/src?at=abc1234"},
		{[]string{"-s"}, repo + "/settings"},
	} {
		args := append([]string{"browse", "-R", "PROJ/repo", "-n"}, tc.args...)
		out, errOut, code := run(t, "", args...)
		if code != 0 || strings.TrimSpace(out) != tc.want {
			t.Errorf("%v: code=%d out=%q err=%q want %q", tc.args, code, out, errOut, tc.want)
		}
	}
	out, _, _ := run(t, "", "browse", "-R", "~Alice/repo", "-n")
	if strings.TrimSpace(out) != f.srv.URL+"/users/alice/repos/repo/browse" {
		t.Errorf("personal repo: %q", out)
	}
	if _, errOut, code := run(t, "", "browse", "-R", "PROJ/repo", "-n", "x.go:abc"); code == 0 || !strings.Contains(errOut, "invalid line") {
		t.Errorf("expected line error, got %q", errOut)
	}
}

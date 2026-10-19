package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
)

func commentURL(pr *bitbucket.PullRequest, id int) string {
	return fmt.Sprintf("%s/overview?commentId=%d", pr.WebURL(), id)
}

func postComment(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest, payload map[string]any) (*bitbucket.Comment, error) {
	var cm bitbucket.Comment
	return &cm, c.Do("POST", r.PRPath(pr.ID)+"/comments", nil, payload, &cm)
}

func newPRCommentCmd(repoFlag *string) *cobra.Command {
	var spec commentSpec
	var bodyFile, suggestion string
	var resolve, reopen bool
	cmd := &cobra.Command{
		Use:   "comment [<number> | <url> | <branch>]",
		Short: "Add a comment to a pull request",
		Long: `Add a comment to a pull request.

Beyond gh, inline and threaded comments are supported:
  --path FILE               comment on a file
  --path FILE --line N      comment on a line. N is the NEW-file line number; the
                            line type (added/context) is inferred from the diff.
                            The line is validated before posting; on failure the
                            commentable lines are listed.
  --line-type removed       N is an OLD-file line number of a removed line
  --suggestion CODE         propose replacement code for the line (renders as an
                            applicable suggestion on recent Bitbucket versions)
  --task                    create the comment as a blocking task
  --reply-to ID             reply to a comment (IDs: bb pr threads)
  --resolve / --reopen      with --reply-to: resolve or reopen that task or
                            thread; the body is optional

Line numbers are easiest to take from "bb pr diff <pr> -n".
The comment URL is printed on success.`,
		Example: `  $ bb pr comment 12 --body "Looks good overall"
  $ bb pr comment 12 -b "Off-by-one?" --path pkg/x.go --line 42
  $ bb pr comment 12 -b "Use the constant" --path pkg/x.go --line 42 --suggestion '	return maxRetries'
  $ bb pr comment 12 -b "Old code was safer" --path pkg/x.go --line 40 --line-type removed
  $ bb pr comment 12 --reply-to 1234 -b "Fixed in 3f2a1c9" --resolve
  $ git diff | bb pr comment 12 -F -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _, err := readBody(spec.Body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			spec.Body = body
			if cmd.Flags().Changed("suggestion") {
				spec.Suggestion = &suggestion
			}
			if resolve && reopen {
				return usageErr("--resolve and --reopen are mutually exclusive")
			}
			if (resolve || reopen) && spec.ReplyTo == 0 {
				return usageErr("--resolve/--reopen need --reply-to <comment-id>")
			}
			post := strings.TrimSpace(spec.Body) != "" || spec.Suggestion != nil
			if !post && !resolve && !reopen {
				return usageErr("comment body required: use --body or --body-file")
			}
			if spec.ReplyTo > 0 && spec.Path != "" {
				return usageErr("--reply-to cannot be combined with --path (replies inherit the thread's location)")
			}
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			if post {
				payload, err := newDiffCache(c, r, pr.ID).commentPayload(spec)
				if err != nil {
					return err
				}
				cm, err := postComment(c, r, pr, payload)
				if err != nil {
					return err
				}
				fmt.Fprintln(stdout, commentURL(pr, cm.ID))
			}
			if resolve || reopen {
				what, err := setResolved(c, r, pr, spec.ReplyTo, resolve)
				if err != nil {
					return err
				}
				verb := "Resolved"
				if reopen {
					verb = "Reopened"
				}
				success("%s %s on %s#%d", verb, what, r.FullName(), pr.ID)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&spec.Body, "body", "b", "", "The comment body `text`")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	f.StringVar(&spec.Path, "path", "", "File path for an inline comment")
	f.IntVar(&spec.Line, "line", 0, "Line number for an inline comment (new file; old file with --line-type removed)")
	f.StringVar(&spec.LineType, "line-type", "", "Line type: {added|removed|context} (default: inferred from the diff)")
	f.StringVar(&suggestion, "suggestion", "", "Suggested replacement `code` for the commented line")
	f.IntVar(&spec.ReplyTo, "reply-to", 0, "Reply to the comment with this `id`")
	f.BoolVar(&spec.Task, "task", false, "Create the comment as a blocking task")
	f.BoolVar(&resolve, "resolve", false, "Resolve the task or thread given by --reply-to")
	f.BoolVar(&reopen, "reopen", false, "Reopen the task or thread given by --reply-to")
	return cmd
}

// setResolved resolves/reopens a task (by its own state) or a thread (on its
// root comment, so a reply ID works too).
func setResolved(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest, id int, resolved bool) (string, error) {
	roots, err := c.Comments(r.Project, r.Slug, pr.ID)
	if err != nil {
		return "", err
	}
	var target *bitbucket.Comment
	var root *bitbucket.Comment
	var find func(cs []bitbucket.Comment, top *bitbucket.Comment) bool
	find = func(cs []bitbucket.Comment, top *bitbucket.Comment) bool {
		for i := range cs {
			t := top
			if t == nil {
				t = &cs[i]
			}
			if cs[i].ID == id {
				target, root = &cs[i], t
				return true
			}
			if find(cs[i].Comments, t) {
				return true
			}
		}
		return false
	}
	if !find(roots, nil) {
		return "", fmt.Errorf("comment %d not found on %s#%d", id, r.FullName(), pr.ID)
	}
	isTask := target.Severity == "BLOCKER"
	if !isTask {
		target = root
	}
	fresh, err := c.GetComment(r.Project, r.Slug, pr.ID, target.ID)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"version": fresh.Version}
	what := fmt.Sprintf("thread %d", target.ID)
	if isTask {
		state := "OPEN"
		if resolved {
			state = "RESOLVED"
		}
		payload["state"] = state
		what = fmt.Sprintf("task %d", target.ID)
	} else {
		payload["threadResolved"] = resolved
	}
	path := r.PRPath(pr.ID) + "/comments/" + strconv.Itoa(target.ID)
	return what, c.Do("PUT", path, nil, payload, nil)
}

// reviewFile is the --input format for `bb pr review`, modelled on GitHub's
// review API (event, body, comments).
type reviewFile struct {
	Event    string        `json:"event"`
	Body     string        `json:"body"`
	Comments []commentSpec `json:"comments"`
}

func newPRReviewCmd(repoFlag *string) *cobra.Command {
	var approve, requestChanges, comment, dryRun bool
	var body, bodyFile, input string
	cmd := &cobra.Command{
		Use:   "review [<number> | <url> | <branch>]",
		Short: "Add a review to a pull request",
		Long: `Add a review to a pull request.

--approve sets your status to Approved, --request-changes to Needs work.
A body, if given, is posted as a general (summary) comment.

--input FILE (or "-" for stdin) submits a whole review in one step:

  {
    "event": "REQUEST_CHANGES",            // APPROVE | REQUEST_CHANGES | COMMENT
    "body": "Summary of the review",
    "comments": [
      {"path": "src/a.go", "line": 42, "body": "Nil deref if cfg is missing", "task": true},
      {"path": "src/a.go", "line": 50, "body": "Prefer the constant", "suggestion": "\treturn maxRetries"},
      {"path": "src/old.go", "line": 7, "lineType": "removed", "body": "Was this intended?"},
      {"path": "docs/x.md", "body": "File-level comment"},
      {"body": "General comment"}
    ]
  }

Line numbers follow "bb pr comment": new-file numbers, or old-file numbers
with "lineType": "removed" (take them from "bb pr diff <pr> -n").
"task": true makes a blocking task the author must resolve.

Every comment is validated against the diff first; if any is invalid,
nothing is posted and all problems are listed. --dry-run only validates.
Comments are posted, then the summary, then the status. Each posted comment
is printed as "<location>\t<url>".`,
		Example: `  $ bb pr review --approve
  $ bb pr review --comment -b "interesting"
  $ bb pr review 12 -r -b "needs more ASCII art"
  $ bb pr review 12 --input review.json --dry-run && bb pr review 12 --input review.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, b := range []bool{approve, requestChanges, comment} {
				if b {
					n++
				}
			}
			if n > 1 {
				return usageErr("only one of --approve, --request-changes, or --comment can be used")
			}
			var rf reviewFile
			if input != "" {
				var data []byte
				var err error
				if input == "-" {
					data, err = io.ReadAll(stdin)
				} else {
					data, err = os.ReadFile(input)
				}
				if err != nil {
					return err
				}
				dec := json.NewDecoder(strings.NewReader(string(data)))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&rf); err != nil {
					return fmt.Errorf("parsing review input: %w", err)
				}
			}
			switch {
			case approve:
				rf.Event = "APPROVE"
			case requestChanges:
				rf.Event = "REQUEST_CHANGES"
			case comment:
				rf.Event = "COMMENT"
			case rf.Event == "" && input != "":
				rf.Event = "COMMENT"
			}
			rf.Event = strings.ToUpper(rf.Event)
			if rf.Event != "APPROVE" && rf.Event != "REQUEST_CHANGES" && rf.Event != "COMMENT" {
				return usageErr("need one of --approve, --request-changes, --comment (or \"event\" in --input)")
			}
			b, given, err := readBody(body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			if given {
				rf.Body = b
			}
			if rf.Event == "COMMENT" && strings.TrimSpace(rf.Body) == "" && len(rf.Comments) == 0 {
				return usageErr("body cannot be blank for comment review")
			}
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}

			dc := newDiffCache(c, r, pr.ID)
			payloads := make([]map[string]any, len(rf.Comments))
			var problems []string
			for i, s := range rf.Comments {
				p, err := dc.commentPayload(s)
				if err != nil {
					problems = append(problems, fmt.Sprintf("comments[%d] (%s): %v", i, s.location(), err))
					continue
				}
				payloads[i] = p
			}
			if len(problems) > 0 {
				return fmt.Errorf("review not submitted, %d of %d comments are invalid:\n%s", len(problems), len(rf.Comments), strings.Join(problems, "\n"))
			}
			tasks := 0
			for _, s := range rf.Comments {
				if s.Task {
					tasks++
				}
			}
			ref := fmt.Sprintf("%s#%d", r.FullName(), pr.ID)
			if dryRun {
				for _, s := range rf.Comments {
					kind := "comment"
					if s.Task {
						kind = "task"
					}
					fmt.Fprintf(stdout, "%s\t%s\n", s.location(), kind)
				}
				success("Review for %s is valid: %s, %d comments (%d tasks); nothing posted", ref, rf.Event, len(rf.Comments), tasks)
				return nil
			}

			for i, p := range payloads {
				cm, err := postComment(c, r, pr, p)
				if err != nil {
					return fmt.Errorf("posting comments[%d] (%s) failed: %w\ncomments[0..%d] were posted; resubmit only comments[%d:] (without the summary/event if already sent)", i, rf.Comments[i].location(), err, i-1, i)
				}
				fmt.Fprintf(stdout, "%s\t%s\n", rf.Comments[i].location(), commentURL(pr, cm.ID))
			}
			if strings.TrimSpace(rf.Body) != "" {
				cm, err := postComment(c, r, pr, map[string]any{"text": rf.Body})
				if err != nil {
					return fmt.Errorf("posting summary failed (all %d inline comments were posted): %w", len(payloads), err)
				}
				fmt.Fprintf(stdout, "summary\t%s\n", commentURL(pr, cm.ID))
			}
			if rf.Event != "COMMENT" {
				me, err := c.CurrentUser()
				if err != nil {
					return err
				}
				status := "APPROVED"
				if rf.Event == "REQUEST_CHANGES" {
					status = "NEEDS_WORK"
				}
				payload := map[string]any{"user": map[string]any{"name": me}, "approved": status == "APPROVED", "status": status}
				if err := c.Do("PUT", r.PRPath(pr.ID)+"/participants/"+url.PathEscape(me), nil, payload, nil); err != nil {
					return err
				}
			}
			verb := map[string]string{"APPROVE": "Approved", "REQUEST_CHANGES": "Requested changes to", "COMMENT": "Reviewed"}[rf.Event]
			success("%s pull request %s at %s (%d comments, %d tasks)", verb, ref, short(pr.FromRef.LatestCommit), len(rf.Comments), tasks)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&approve, "approve", "a", false, "Approve pull request")
	f.BoolVarP(&requestChanges, "request-changes", "r", false, "Request changes on a pull request (Needs work)")
	f.BoolVarP(&comment, "comment", "c", false, "Comment on a pull request")
	f.StringVarP(&body, "body", "b", "", "Specify the body of a review")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	f.StringVar(&input, "input", "", "Submit a whole review from a JSON `file` (\"-\" for stdin)")
	f.BoolVar(&dryRun, "dry-run", false, "Validate the review without posting anything")
	return cmd
}

// thread is one item of feedback: a comment thread or a task, flattened for agents.
type thread struct {
	ID          int              `json:"id"`
	Kind        string           `json:"kind"` // "task" or "thread"
	State       string           `json:"state"`
	Path        string           `json:"path,omitempty"`
	Line        int              `json:"line,omitempty"`
	LineType    string           `json:"lineType,omitempty"`
	Outdated    bool             `json:"outdated"`
	Author      string           `json:"author"`
	Body        string           `json:"body"`
	CreatedAt   string           `json:"createdAt"`
	Replies     []map[string]any `json:"replies"`
	OpenTasks   []map[string]any `json:"openTasks"`
	LastBy      string           `json:"lastCommentBy"`
	NeedsReply  bool             `json:"needsReply"`
	URL         string           `json:"url"`
	Context     []string         `json:"context,omitempty"`
	anchor      *bitbucket.Anchor
	createdUnix int64
}

func (t thread) open() bool { return t.State == "open" || len(t.OpenTasks) > 0 }

// buildThreads flattens the PR's comments into threads, oldest first.
func buildThreads(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest) ([]thread, error) {
	roots, err := c.Comments(r.Project, r.Slug, pr.ID)
	if err != nil {
		return nil, err
	}
	me, err := c.CurrentUser()
	if err != nil {
		return nil, err
	}
	var out []thread
	for _, root := range roots {
		t := thread{
			ID: root.ID, Kind: "thread", State: "open", Author: root.Author.Slug, Body: root.Text,
			CreatedAt: ts(root.CreatedDate), URL: commentURL(pr, root.ID), anchor: root.Anchor,
			createdUnix: root.CreatedDate, Replies: []map[string]any{}, OpenTasks: []map[string]any{},
		}
		if root.Severity == "BLOCKER" {
			t.Kind = "task"
			if root.State == "RESOLVED" {
				t.State = "resolved"
			}
		} else if root.ThreadResolved {
			t.State = "resolved"
		}
		if a := root.Anchor; a != nil {
			t.Path, t.Line, t.LineType, t.Outdated = a.Path, a.Line, strings.ToLower(a.LineType), a.Orphaned
		}
		last := root
		var walk func(cs []bitbucket.Comment)
		walk = func(cs []bitbucket.Comment) {
			for _, cm := range cs {
				t.Replies = append(t.Replies, map[string]any{
					"id": cm.ID, "author": cm.Author.Slug, "body": cm.Text, "createdAt": ts(cm.CreatedDate),
					"isTask": cm.Severity == "BLOCKER", "state": strings.ToLower(cm.State),
				})
				if cm.Severity == "BLOCKER" && cm.State == "OPEN" {
					t.OpenTasks = append(t.OpenTasks, map[string]any{"id": cm.ID, "body": cm.Text})
				}
				if cm.CreatedDate >= last.CreatedDate {
					last = cm
				}
				walk(cm.Comments)
			}
		}
		walk(root.Comments)
		sort.SliceStable(t.Replies, func(i, j int) bool { return t.Replies[i]["createdAt"].(string) < t.Replies[j]["createdAt"].(string) })
		t.LastBy = last.Author.Slug
		t.NeedsReply = t.LastBy != me
		out = append(out, t)
	}
	return out, nil
}

// addContext attaches the code the comment was made on, from the commit the
// anchor refers to (falling back to the PR head for new-file lines).
func addContext(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest, items []thread, n int) {
	cache := map[string][]string{}
	for i := range items {
		t := &items[i]
		a := t.anchor
		if n <= 0 || a == nil || a.Line <= 0 || a.Orphaned {
			continue
		}
		at, path := a.ToHash, a.Path
		if a.FileType == "FROM" {
			at = a.FromHash
			if a.SrcPath != "" {
				path = a.SrcPath
			}
		}
		if at == "" && a.FileType != "FROM" {
			at = pr.FromRef.LatestCommit
		}
		if at == "" {
			continue
		}
		key := at + ":" + path
		lines, ok := cache[key]
		if !ok {
			content, err := c.RawFile(r.Project, r.Slug, path, at)
			if err == nil {
				lines = strings.Split(content, "\n")
			}
			cache[key] = lines
		}
		if len(lines) == 0 {
			continue
		}
		for ln := a.Line - n; ln <= a.Line+n; ln++ {
			if ln < 1 || ln > len(lines) {
				continue
			}
			mark := " "
			if ln == a.Line {
				mark = ">"
			}
			t.Context = append(t.Context, fmt.Sprintf("%s%5d │ %s", mark, ln, lines[ln-1]))
		}
	}
}

var threadFields = []string{
	"author", "body", "context", "createdAt", "id", "kind", "lastCommentBy", "line", "lineType",
	"needsReply", "openTasks", "outdated", "path", "replies", "state", "url",
}

func newPRThreadsCmd(repoFlag *string) *cobra.Command {
	var all, needsReply bool
	var startedBy string
	var contextLines int
	var exp exporter
	cmd := &cobra.Command{
		Use:   "threads [<number> | <url> | <branch>]",
		Short: "List open review threads and tasks on a pull request",
		Long: `List comment threads and tasks on a pull request: the work queue for
addressing feedback, and the follow-up list when re-reviewing.

By default only open items are shown: unresolved threads, open tasks, and
resolved threads that still contain an open task. Each item shows its ID
(for "bb pr comment --reply-to ID [--resolve]"), file:line anchor, the code
the comment was made on, all replies, and whether it needs a reply (the
last comment is not yours).

"outdated" means the commented line no longer exists in the current diff.`,
		Example: `  $ bb pr threads 42
  $ bb pr threads 42 --needs-reply
  $ bb pr threads 42 --started-by @me --needs-reply    # replies to my review
  $ bb pr threads 42 --json id,kind,path,line,body,replies,context`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			threads, err := buildThreads(c, r, pr)
			if err != nil {
				return err
			}
			if startedBy != "" {
				if startedBy, err = meOr(c, startedBy); err != nil {
					return err
				}
			}
			var sel []thread
			for _, t := range threads {
				if (!all && !t.open()) || (needsReply && !t.NeedsReply) || (startedBy != "" && !strings.EqualFold(t.Author, startedBy)) {
					continue
				}
				sel = append(sel, t)
			}
			// Group by file so related items are handled together; general comments last.
			sort.SliceStable(sel, func(i, j int) bool {
				a, b := sel[i], sel[j]
				if (a.Path == "") != (b.Path == "") {
					return a.Path != ""
				}
				if a.Path != b.Path {
					return a.Path < b.Path
				}
				return a.Line < b.Line
			})
			if exp.enabled() && !exp.wants("context") {
				contextLines = 0
			}
			addContext(c, r, pr, sel, contextLines)
			if exp.enabled() {
				out := []map[string]any{}
				for _, t := range sel {
					raw, _ := json.Marshal(t)
					var m map[string]any
					_ = json.Unmarshal(raw, &m)
					out = append(out, pick(m, exp.fields))
				}
				return exp.write(stdout, out)
			}
			printThreads(pr, sel)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&all, "all", false, "Include resolved threads and tasks")
	f.BoolVar(&needsReply, "needs-reply", false, "Only items whose last comment is not yours")
	f.StringVar(&startedBy, "started-by", "", "Only threads started by this user (\"@me\" for yourself)")
	f.IntVarP(&contextLines, "context", "C", 3, "Lines of code context around anchored comments (0 to disable)")
	addJSONFlags(cmd, &exp, threadFields)
	return cmd
}

func printThreads(pr *bitbucket.PullRequest, items []thread) {
	reply := 0
	for _, t := range items {
		if t.NeedsReply {
			reply++
		}
		head := []string{bold(fmt.Sprintf("#%d", t.ID)), strings.ToUpper(t.Kind), t.State}
		if t.Path != "" {
			loc := t.Path
			if t.Line > 0 {
				loc += fmt.Sprintf(":%d (%s)", t.Line, t.LineType)
			}
			head = append(head, cyan(loc))
		} else {
			head = append(head, "general")
		}
		if t.Outdated {
			head = append(head, yellow("outdated"))
		}
		head = append(head, "by "+t.Author)
		if t.NeedsReply {
			head = append(head, yellow("needs reply"))
		}
		fmt.Fprintln(stdout, strings.Join(head, " · "))
		for _, l := range strings.Split(t.Body, "\n") {
			fmt.Fprintln(stdout, "  "+l)
		}
		for _, l := range t.Context {
			fmt.Fprintln(stdout, "    "+gray(l))
		}
		for _, rp := range t.Replies {
			tag := ""
			if rp["isTask"] == true {
				tag = fmt.Sprintf(" [task %s]", rp["state"])
			}
			lines := strings.Split(rp["body"].(string), "\n")
			fmt.Fprintf(stdout, "  ↳ #%d %s%s: %s\n", rp["id"], rp["author"], tag, lines[0])
			for _, l := range lines[1:] {
				fmt.Fprintln(stdout, "      "+l)
			}
		}
		fmt.Fprintln(stdout)
	}
	if len(items) == 0 {
		fmt.Fprintf(stdout, "No matching threads or tasks on #%d.\n", pr.ID)
		return
	}
	fmt.Fprintf(stdout, "%d item(s) on #%d, %d need a reply. Reply/resolve: bb pr comment %d --reply-to <id> -b \"...\" [--resolve]\n", len(items), pr.ID, reply, pr.ID)
}

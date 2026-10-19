package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/git"
)

func refJSON(branch string, r Repo) map[string]any {
	return map[string]any{
		"id":         "refs/heads/" + branch,
		"repository": map[string]any{"slug": r.Slug, "project": map[string]any{"key": r.Project}},
	}
}

func reviewersJSON(names []string) []map[string]any {
	out := []map[string]any{}
	for _, n := range names {
		out = append(out, map[string]any{"user": map[string]any{"name": n}})
	}
	return out
}

// splitList flattens repeated and comma-separated flag values.
func splitList(vs []string) []string {
	var out []string
	for _, v := range vs {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func resolveUsers(c *bitbucket.Client, qs []string) ([]string, error) {
	var names []string
	for _, q := range qs {
		q, err := meOr(c, q)
		if err != nil {
			return nil, err
		}
		u, err := c.ResolveUser(q)
		if err != nil {
			return nil, fmt.Errorf("reviewer %q: %w", q, err)
		}
		names = append(names, u.Name)
	}
	return names, nil
}

func defaultReviewers(c *bitbucket.Client, r Repo, head, base string) ([]string, error) {
	repo, err := c.GetRepo(r.Project, r.Slug)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"sourceRepoId": {strconv.Itoa(repo.ID)}, "targetRepoId": {strconv.Itoa(repo.ID)},
		"sourceRefId": {"refs/heads/" + head}, "targetRefId": {"refs/heads/" + base},
	}
	var users []bitbucket.User
	path := "rest/default-reviewers/1.0/projects/" + url.PathEscape(r.Project) + "/repos/" + url.PathEscape(r.Slug) + "/reviewers"
	if err := c.Do("GET", path, q, nil, &users); err != nil {
		return nil, err
	}
	var names []string
	for _, u := range users {
		names = append(names, u.Name)
	}
	return names, nil
}

func dedupe(xs []string, exclude string) []string {
	seen := map[string]bool{strings.ToLower(exclude): true}
	var out []string
	for _, x := range xs {
		if !seen[strings.ToLower(x)] {
			seen[strings.ToLower(x)] = true
			out = append(out, x)
		}
	}
	return out
}

func newPRCreateCmd(repoFlag *string) *cobra.Command {
	var title, body, bodyFile, base, head string
	var reviewers []string
	var draft, fill, fillFirst, dryRun, noDefaultReviewers, web bool
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a pull request",
		Aliases: []string{"new"},
		Long: `Create a pull request on Bitbucket.

The head branch must already be pushed. Repository default reviewers are added
automatically (as the Bitbucket UI does) unless --no-default-reviewers is set.

Reviewers can be given as username, slug, email, or a unique name fragment.`,
		Example: `  $ bb pr create --title "Fix bug" --body "Details" --reviewer alice,bob
  $ bb pr create --fill --base develop --draft
  $ bb pr create -t "Add feature" -F body.md`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			if head == "" {
				if head, err = git.CurrentBranch(); err != nil {
					return fmt.Errorf("%w; use --head", err)
				}
			}
			if base == "" {
				if base, err = c.DefaultBranch(r.Project, r.Slug); err != nil {
					return fmt.Errorf("determining default branch: %w", err)
				}
			}
			if head == base {
				return fmt.Errorf("head branch %q is the same as base branch", head)
			}
			body, bodyGiven, err := readBody(body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			if fill || fillFirst {
				t, b, err := commitSummary(r, base, head, fillFirst)
				if err != nil {
					return err
				}
				if title == "" {
					title = t
				}
				if !bodyGiven {
					body = b
				}
			}
			if title == "" {
				if !isTTY() {
					return usageErr("must provide `--title` and `--body` (or `--fill`) when not running interactively")
				}
				title = prompt("Title: ")
				if !bodyGiven && !fill {
					body = prompt("Body: ")
				}
			}
			if title == "" {
				return errors.New("title cannot be blank")
			}
			if ok, err := c.BranchExists(r.Project, r.Slug, head); err != nil {
				return err
			} else if !ok {
				remote := r.Remote
				if remote == "" {
					remote = "origin"
				}
				return fmt.Errorf("branch %q not found in %s; push it first: git push -u %s %s", head, r.FullName(), remote, head)
			}
			existing, err := bitbucket.Paginate(c, r.Path()+"/pull-requests",
				url.Values{"state": {"OPEN"}, "direction": {"OUTGOING"}, "at": {"refs/heads/" + head}}, 1,
				func(p bitbucket.PullRequest) bool { return p.FromRef.DisplayID == head && p.ToRef.DisplayID == base })
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				return fmt.Errorf("a pull request for branch %q into branch %q already exists:\n%s", head, base, existing[0].WebURL())
			}
			names, err := resolveUsers(c, splitList(reviewers))
			if err != nil {
				return err
			}
			if !noDefaultReviewers {
				def, err := defaultReviewers(c, r, head, base)
				if err != nil && !bitbucket.IsNotFound(err) {
					fmt.Fprintf(stderr, "warning: could not fetch default reviewers: %v\n", err)
				}
				names = append(names, def...)
			}
			me, _ := c.CurrentUser()
			names = dedupe(names, me)
			payload := map[string]any{
				"title": title, "description": body,
				"fromRef": refJSON(head, r), "toRef": refJSON(base, r),
				"reviewers": reviewersJSON(names),
			}
			if draft {
				payload["draft"] = true
			}
			if dryRun {
				b, _ := json.MarshalIndent(payload, "", "  ")
				fmt.Fprintln(stdout, string(b))
				return nil
			}
			if isTTY() {
				fmt.Fprintf(stderr, "\nCreating pull request for %s into %s in %s\n\n", cyan(head), cyan(base), r.FullName())
			}
			var pr bitbucket.PullRequest
			if err := c.Do("POST", r.Path()+"/pull-requests", nil, payload, &pr); err != nil {
				return err
			}
			if web {
				return openBrowser(pr.WebURL())
			}
			fmt.Fprintln(stdout, pr.WebURL())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&title, "title", "t", "", "Title for the pull request")
	f.StringVarP(&body, "body", "b", "", "Body for the pull request")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	f.StringVarP(&base, "base", "B", "", "The `branch` into which you want your code merged (default: repo default branch)")
	f.StringVarP(&head, "head", "H", "", "The `branch` that contains commits for your pull request (default: current branch)")
	f.StringSliceVarP(&reviewers, "reviewer", "r", nil, "Request reviews from people by their `login`")
	f.BoolVarP(&draft, "draft", "d", false, "Mark pull request as a draft (Bitbucket 8.18+)")
	f.BoolVarP(&fill, "fill", "f", false, "Use commit info for title and body")
	f.BoolVar(&fillFirst, "fill-first", false, "Use first commit info for title and body")
	f.BoolVar(&dryRun, "dry-run", false, "Print the request payload instead of creating the pull request")
	f.BoolVar(&noDefaultReviewers, "no-default-reviewers", false, "Do not add the repository's default reviewers")
	f.BoolVarP(&web, "web", "w", false, "Open the created pull request in the browser")
	return cmd
}

// commitSummary mirrors gh's --fill: one commit → its subject/body; many →
// branch name as title and a bullet list of subjects as body.
func commitSummary(r Repo, base, head string, firstOnly bool) (string, string, error) {
	baseRef := base
	if r.Remote != "" {
		baseRef = r.Remote + "/" + base
	}
	out, err := git.Run("log", "--reverse", "--format=%s%x1f%b%x1e", baseRef+".."+head)
	if err != nil {
		return "", "", err
	}
	type commit struct{ subject, body string }
	var commits []commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1f", 2)
		cm := commit{subject: parts[0]}
		if len(parts) > 1 {
			cm.body = strings.TrimSpace(parts[1])
		}
		commits = append(commits, cm)
	}
	if len(commits) == 0 {
		return "", "", fmt.Errorf("no commits between %s and %s", baseRef, head)
	}
	if len(commits) == 1 || firstOnly {
		return commits[0].subject, commits[0].body, nil
	}
	var b strings.Builder
	for _, cm := range commits {
		fmt.Fprintf(&b, "- %s\n", cm.subject)
	}
	t := strings.NewReplacer("-", " ", "_", " ").Replace(head[strings.LastIndex(head, "/")+1:])
	if t != "" {
		t = strings.ToUpper(t[:1]) + t[1:]
	}
	return t, strings.TrimRight(b.String(), "\n"), nil
}

// updatePR PUTs the PR back with modifications; Bitbucket requires the full
// reviewer list and the current version.
func updatePR(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest, mutate func(map[string]any)) (*bitbucket.PullRequest, error) {
	var names []string
	for _, rv := range pr.Reviewers {
		names = append(names, rv.User.Name)
	}
	payload := map[string]any{
		"version": pr.Version, "title": pr.Title, "description": pr.Description,
		"reviewers": reviewersJSON(names), "toRef": map[string]any{"id": pr.ToRef.ID},
	}
	mutate(payload)
	var out bitbucket.PullRequest
	return &out, c.Do("PUT", r.PRPath(pr.ID), nil, payload, &out)
}

func newPREditCmd(repoFlag *string) *cobra.Command {
	var title, body, bodyFile, base string
	var addReviewers, removeReviewers []string
	cmd := &cobra.Command{
		Use:   "edit [<number> | <url> | <branch>]",
		Short: "Edit a pull request",
		Example: `  $ bb pr edit 23 --title "I found a bug" --body "Nothing works"
  $ bb pr edit 23 --add-reviewer alice,bob --remove-reviewer carol`,
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
			body, bodyGiven, err := readBody(body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			add, err := resolveUsers(c, splitList(addReviewers))
			if err != nil {
				return err
			}
			remove, err := resolveUsers(c, splitList(removeReviewers))
			if err != nil {
				return err
			}
			if title == "" && !bodyGiven && base == "" && len(add) == 0 && len(remove) == 0 {
				return usageErr("specify at least one of --title, --body, --body-file, --base, --add-reviewer, --remove-reviewer")
			}
			updated, err := updatePR(c, r, pr, func(p map[string]any) {
				if title != "" {
					p["title"] = title
				}
				if bodyGiven {
					p["description"] = body
				}
				if base != "" {
					p["toRef"] = map[string]any{"id": "refs/heads/" + base}
				}
				var names []string
				for _, rv := range pr.Reviewers {
					if !contains(remove, rv.User.Name) {
						names = append(names, rv.User.Name)
					}
				}
				p["reviewers"] = reviewersJSON(dedupe(append(names, add...), pr.Author.User.Name))
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, updated.WebURL())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&title, "title", "t", "", "Set the new title")
	f.StringVarP(&body, "body", "b", "", "Set the new body")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	f.StringVarP(&base, "base", "B", "", "Change the base `branch` for this pull request")
	f.StringSliceVar(&addReviewers, "add-reviewer", nil, "Add reviewers by their `login`")
	f.StringSliceVar(&removeReviewers, "remove-reviewer", nil, "Remove reviewers by their `login`")
	return cmd
}

func newPRReadyCmd(repoFlag *string) *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "ready [<number> | <url> | <branch>]",
		Short: "Mark a pull request as ready for review (Bitbucket 8.18+)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			if _, err := updatePR(c, r, pr, func(p map[string]any) { p["draft"] = undo }); err != nil {
				return err
			}
			if undo {
				success("Pull request %s#%d is converted to \"draft\"", r.FullName(), pr.ID)
			} else {
				success("Pull request %s#%d is marked as \"ready for review\"", r.FullName(), pr.ID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "Convert a pull request to \"draft\"")
	return cmd
}

func newPRCommentCmd(repoFlag *string) *cobra.Command {
	var body, bodyFile, path, lineType string
	var line, replyTo int
	var task bool
	cmd := &cobra.Command{
		Use:   "comment [<number> | <url> | <branch>]",
		Short: "Add a comment to a pull request",
		Long: `Add a comment to a pull request.

Beyond gh, inline and threaded comments are supported:
  --path FILE               comment on a file
  --path FILE --line N      comment on a line; N refers to the new file for
                            added/context lines and to the old file for removed lines
  --line-type               added (default), removed or context
  --reply-to ID             reply to an existing comment (IDs: bb pr view --comments)
  --task                    create the comment as a blocking task

The comment URL is printed on success.`,
		Example: `  $ bb pr comment 12 --body "Looks good overall"
  $ bb pr comment 12 -b "Off-by-one?" --path pkg/x.go --line 42
  $ bb pr comment 12 -b "Old code was safer" --path pkg/x.go --line 40 --line-type removed
  $ bb pr comment 12 -b "Fixed" --reply-to 1234
  $ git diff | bb pr comment 12 -F -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _, err := readBody(body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			if strings.TrimSpace(body) == "" {
				return usageErr("comment body required: use --body or --body-file")
			}
			if line > 0 && path == "" {
				return usageErr("--line requires --path")
			}
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			payload := map[string]any{"text": body}
			if replyTo > 0 {
				payload["parent"] = map[string]any{"id": replyTo}
			}
			if task {
				payload["severity"] = "BLOCKER"
			}
			if path != "" {
				anchor := map[string]any{"path": path, "diffType": "EFFECTIVE"}
				if line > 0 {
					lt := strings.ToUpper(lineType)
					if lt != "ADDED" && lt != "REMOVED" && lt != "CONTEXT" {
						return usageErr("invalid --line-type %q: valid values are {added|removed|context}", lineType)
					}
					ft := "TO"
					if lt == "REMOVED" {
						ft = "FROM"
					}
					anchor["line"], anchor["lineType"], anchor["fileType"] = line, lt, ft
				}
				payload["anchor"] = anchor
			}
			var cm bitbucket.Comment
			if err := c.Do("POST", r.PRPath(pr.ID)+"/comments", nil, payload, &cm); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s/overview?commentId=%d\n", pr.WebURL(), cm.ID)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&body, "body", "b", "", "The comment body `text`")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	f.StringVar(&path, "path", "", "File path for an inline comment")
	f.IntVar(&line, "line", 0, "Line number for an inline comment")
	f.StringVar(&lineType, "line-type", "added", "Line type for an inline comment: {added|removed|context}")
	f.IntVar(&replyTo, "reply-to", 0, "Reply to the comment with this `id`")
	f.BoolVar(&task, "task", false, "Create the comment as a blocking task")
	return cmd
}

func newPRReviewCmd(repoFlag *string) *cobra.Command {
	var approve, requestChanges, comment bool
	var body, bodyFile string
	cmd := &cobra.Command{
		Use:   "review [<number> | <url> | <branch>]",
		Short: "Add a review to a pull request",
		Long: `Add a review to a pull request.

--approve sets your status to Approved, --request-changes to Needs work.
A body, if given, is posted as a general comment.`,
		Example: `  $ bb pr review --approve
  $ bb pr review --comment -b "interesting"
  $ bb pr review 12 -r -b "needs more ASCII art"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, b := range []bool{approve, requestChanges, comment} {
				if b {
					n++
				}
			}
			if n != 1 {
				return usageErr("need exactly one of --approve, --request-changes, or --comment")
			}
			body, _, err := readBody(body, bodyFile, cmd.Flags())
			if err != nil {
				return err
			}
			if comment && strings.TrimSpace(body) == "" {
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
			if strings.TrimSpace(body) != "" {
				if err := c.Do("POST", r.PRPath(pr.ID)+"/comments", nil, map[string]any{"text": body}, nil); err != nil {
					return err
				}
			}
			ref := fmt.Sprintf("%s#%d", r.FullName(), pr.ID)
			if comment {
				success("Reviewed pull request %s", ref)
				return nil
			}
			me, err := c.CurrentUser()
			if err != nil {
				return err
			}
			status := "APPROVED"
			if requestChanges {
				status = "NEEDS_WORK"
			}
			payload := map[string]any{"user": map[string]any{"name": me}, "approved": approve, "status": status}
			if err := c.Do("PUT", r.PRPath(pr.ID)+"/participants/"+url.PathEscape(me), nil, payload, nil); err != nil {
				return err
			}
			if approve {
				success("Approved pull request %s", ref)
			} else {
				success("Requested changes to pull request %s", ref)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&approve, "approve", "a", false, "Approve pull request")
	f.BoolVarP(&requestChanges, "request-changes", "r", false, "Request changes on a pull request (Needs work)")
	f.BoolVarP(&comment, "comment", "c", false, "Comment on a pull request")
	f.StringVarP(&body, "body", "b", "", "Specify the body of a review")
	f.StringVarP(&bodyFile, "body-file", "F", "", "Read body text from `file` (use \"-\" to read from standard input)")
	return cmd
}

func newPRMergeCmd(repoFlag *string) *cobra.Command {
	var merge, squash, rebase, deleteBranch bool
	var subject, body string
	cmd := &cobra.Command{
		Use:   "merge [<number> | <url> | <branch>]",
		Short: "Merge a pull request",
		Long: `Merge a pull request on Bitbucket.

Without a strategy flag the repository's default merge strategy is used.
--merge → no-ff, --squash → squash, --rebase → rebase-no-ff
(the strategy must be enabled for the repository).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			strategy := ""
			n := 0
			for s, on := range map[string]bool{"no-ff": merge, "squash": squash, "rebase-no-ff": rebase} {
				if on {
					strategy = s
					n++
				}
			}
			if n > 1 {
				return usageErr("only one of --merge, --squash, --rebase can be enabled")
			}
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			if pr.State != "OPEN" {
				return fmt.Errorf("pull request %s#%d is not open (%s)", r.FullName(), pr.ID, ghState(pr))
			}
			ms, err := c.MergeStatus(r.Project, r.Slug, pr.ID)
			if err != nil {
				return err
			}
			if !ms.CanMerge {
				var reasons []string
				if ms.Conflicted {
					reasons = append(reasons, "the pull request has merge conflicts")
				}
				for _, v := range ms.Vetoes {
					reasons = append(reasons, strings.TrimSpace(v.SummaryMessage+": "+v.DetailedMessage))
				}
				return fmt.Errorf("pull request %s#%d is not mergeable:\n  - %s", r.FullName(), pr.ID, strings.Join(reasons, "\n  - "))
			}
			payload := map[string]any{}
			if strategy != "" {
				payload["strategyId"] = strategy
			}
			if msg := strings.TrimSpace(strings.TrimSpace(subject) + "\n\n" + body); msg != "" {
				payload["message"] = msg
			}
			q := url.Values{"version": {strconv.Itoa(pr.Version)}}
			if err := c.Do("POST", r.PRPath(pr.ID)+"/merge", q, payload, nil); err != nil {
				return err
			}
			success("Merged pull request %s#%d (%s)", r.FullName(), pr.ID, pr.Title)
			if deleteBranch {
				return deletePRBranch(c, r, pr)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&merge, "merge", "m", false, "Merge the commits with the base branch (no-ff)")
	f.BoolVarP(&squash, "squash", "s", false, "Squash the commits into one commit and merge it into the base branch")
	f.BoolVarP(&rebase, "rebase", "r", false, "Rebase the commits onto the base branch")
	f.BoolVarP(&deleteBranch, "delete-branch", "d", false, "Delete the local and remote branch after merge")
	f.StringVarP(&subject, "subject", "t", "", "Subject `text` for the merge commit")
	f.StringVarP(&body, "body", "b", "", "Body `text` for the merge commit")
	return cmd
}

// deletePRBranch removes the head branch remotely (same-repo PRs only) and locally.
func deletePRBranch(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest) error {
	head := pr.FromRef.DisplayID
	if !pr.IsCrossRepository() {
		path := "rest/branch-utils/1.0/projects/" + url.PathEscape(r.Project) + "/repos/" + url.PathEscape(r.Slug) + "/branches"
		if err := c.Do("DELETE", path, nil, map[string]any{"name": pr.FromRef.ID, "dryRun": false}, nil); err != nil {
			return fmt.Errorf("deleting remote branch %s: %w", head, err)
		}
		success("Deleted remote branch %s", head)
	}
	if r.Remote == "" || !git.HasLocalBranch(head) {
		return nil
	}
	if cur, _ := git.CurrentBranch(); cur == head {
		if _, err := git.Run("checkout", pr.ToRef.DisplayID); err != nil {
			return err
		}
	}
	if _, err := git.Run("branch", "-D", head); err != nil {
		return err
	}
	success("Deleted local branch %s", head)
	return nil
}

func prTransition(repoFlag *string, use, short, action, verb string) *cobra.Command {
	var comment string
	var deleteBranch bool
	cmd := &cobra.Command{
		Use:   use + " [<number> | <url> | <branch>]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			if comment != "" {
				if err := c.Do("POST", r.PRPath(pr.ID)+"/comments", nil, map[string]any{"text": comment}, nil); err != nil {
					return err
				}
			}
			q := url.Values{"version": {strconv.Itoa(pr.Version)}}
			if err := c.Do("POST", r.PRPath(pr.ID)+"/"+action, q, map[string]any{}, nil); err != nil {
				return err
			}
			success("%s pull request %s#%d (%s)", verb, r.FullName(), pr.ID, pr.Title)
			if deleteBranch {
				return deletePRBranch(c, r, pr)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&comment, "comment", "c", "", "Leave a closing comment")
	if action == "decline" {
		cmd.Flags().BoolVarP(&deleteBranch, "delete-branch", "d", false, "Delete the local and remote branch after close")
	}
	return cmd
}

func newPRCloseCmd(repoFlag *string) *cobra.Command {
	return prTransition(repoFlag, "close", "Close (decline) a pull request", "decline", "Closed")
}

func newPRReopenCmd(repoFlag *string) *cobra.Command {
	return prTransition(repoFlag, "reopen", "Reopen a closed (declined) pull request", "reopen", "Reopened")
}

package cmd

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
)

func newPRCmd() *cobra.Command {
	var repoFlag string
	cmd := &cobra.Command{
		Use:     "pr <command>",
		Short:   "Manage pull requests",
		Aliases: []string{"pull-request"},
		Long: `Work with Bitbucket pull requests.

A pull request can be supplied as argument in any of the following formats:
- by number, e.g. "123";
- by URL, e.g. "https://bitbucket.example.com/projects/PROJ/repos/repo/pull-requests/123";
- by the name of its head branch, e.g. "feature/foo".
Without an argument, the pull request for the current branch is used.`,
	}
	cmd.PersistentFlags().StringVarP(&repoFlag, "repo", "R", "", "Select another repository using the `[HOST/]PROJECT/REPO` format")
	cmd.AddCommand(
		newPRListCmd(&repoFlag), newPRViewCmd(&repoFlag), newPRStatusCmd(&repoFlag),
		newPRDiffCmd(&repoFlag), newPRChecksCmd(&repoFlag), newPRCheckoutCmd(&repoFlag),
		newPRCreateCmd(&repoFlag), newPREditCmd(&repoFlag), newPRReadyCmd(&repoFlag),
		newPRCommentCmd(&repoFlag), newPRReviewCmd(&repoFlag), newPRThreadsCmd(&repoFlag),
		newPRMergeCmd(&repoFlag), newPRCloseCmd(&repoFlag), newPRReopenCmd(&repoFlag),
	)
	return cmd
}

var prFields = []string{
	"author", "baseRefName", "baseRefOid", "body", "closed", "closedAt", "commentCount", "comments",
	"commits", "createdAt", "files", "headRefName", "headRefOid", "headRepository", "id", "isCrossRepository",
	"isDraft", "latestReviews", "mergeBlockers", "mergeStateStatus", "mergeable", "number", "participants", "reviewDecision",
	"reviewRequests", "state", "statusCheckRollup", "taskCount", "title", "unresolvedThreads", "updatedAt", "url", "version",
}

// ghState maps Bitbucket states to gh's vocabulary.
func ghState(pr *bitbucket.PullRequest) string {
	if pr.State == "DECLINED" {
		return "CLOSED"
	}
	return pr.State
}

func reviewState(status string) string {
	switch status {
	case "APPROVED":
		return "APPROVED"
	case "NEEDS_WORK":
		return "CHANGES_REQUESTED"
	}
	return "PENDING"
}

func reviewDecision(pr *bitbucket.PullRequest) string {
	if len(pr.Reviewers) == 0 {
		return ""
	}
	approved := false
	for _, r := range pr.Reviewers {
		switch r.Status {
		case "NEEDS_WORK":
			return "CHANGES_REQUESTED"
		case "APPROVED":
			approved = true
		}
	}
	if approved {
		return "APPROVED"
	}
	return "REVIEW_REQUIRED"
}

// mergeBlockers explains why a PR can't be merged: conflicts and merge-check
// vetoes (required approvals, builds, open tasks, …). Empty when mergeable.
func mergeBlockers(ms *bitbucket.MergeStatus) []string {
	if ms.CanMerge {
		return nil
	}
	var out []string
	if ms.Conflicted {
		out = append(out, "the pull request has merge conflicts")
	}
	for _, v := range ms.Vetoes {
		msg := strings.TrimSpace(v.SummaryMessage)
		if d := strings.TrimSpace(v.DetailedMessage); d != "" && d != msg {
			msg = strings.TrimSuffix(msg, ".") + ": " + d
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		out = append(out, "Bitbucket reports the pull request as not mergeable")
	}
	return out
}

// mergeStateStatus maps Bitbucket's merge status to gh's vocabulary.
func mergeStateStatus(pr *bitbucket.PullRequest, ms *bitbucket.MergeStatus, err error) string {
	switch {
	case err != nil || pr.State != "OPEN":
		return "UNKNOWN"
	case ms.Conflicted:
		return "DIRTY"
	case ms.CanMerge:
		return "CLEAN"
	case pr.Draft:
		return "DRAFT"
	}
	return "BLOCKED"
}

func userJSON(u bitbucket.User) map[string]any {
	return map[string]any{"login": u.Slug, "name": u.DisplayName, "email": u.EmailAddress}
}

func commentJSON(cm bitbucket.Comment) map[string]any {
	m := map[string]any{
		"id": cm.ID, "author": userJSON(cm.Author), "body": cm.Text,
		"createdAt": ts(cm.CreatedDate), "updatedAt": ts(cm.UpdatedDate),
		"isTask": cm.Severity == "BLOCKER", "state": cm.State, "threadResolved": cm.ThreadResolved,
	}
	if a := cm.Anchor; a != nil {
		m["path"], m["line"], m["lineType"], m["fileType"], m["orphaned"] = a.Path, a.Line, a.LineType, a.FileType, a.Orphaned
	}
	var replies []map[string]any
	for _, r := range cm.Comments {
		replies = append(replies, commentJSON(r))
	}
	m["replies"] = replies
	return m
}

// prJSON builds the --json view. Fields needing extra API calls are only
// fetched when requested.
func prJSON(c *bitbucket.Client, r Repo, pr *bitbucket.PullRequest, e *exporter) (map[string]any, error) {
	var reviewRequests, latestReviews, participants []map[string]any
	for _, rv := range pr.Reviewers {
		reviewRequests = append(reviewRequests, userJSON(rv.User))
		latestReviews = append(latestReviews, map[string]any{"author": userJSON(rv.User), "state": reviewState(rv.Status)})
	}
	for _, p := range pr.Participants {
		participants = append(participants, map[string]any{"user": userJSON(p.User), "role": p.Role, "status": p.Status})
	}
	var headRepo any
	if hr := pr.FromRef.Repository; hr != nil {
		headRepo = map[string]any{"project": hr.Project.Key, "slug": hr.Slug, "name": hr.Name}
	}
	m := map[string]any{
		"id": pr.ID, "number": pr.ID, "version": pr.Version, "title": pr.Title, "body": pr.Description,
		"state": ghState(pr), "isDraft": pr.Draft, "closed": pr.Closed, "closedAt": ts(pr.ClosedDate),
		"createdAt": ts(pr.CreatedDate), "updatedAt": ts(pr.UpdatedDate), "url": pr.WebURL(),
		"author":      userJSON(pr.Author.User),
		"headRefName": pr.FromRef.DisplayID, "headRefOid": pr.FromRef.LatestCommit,
		"baseRefName": pr.ToRef.DisplayID, "baseRefOid": pr.ToRef.LatestCommit,
		"headRepository": headRepo, "isCrossRepository": pr.IsCrossRepository(),
		"reviewRequests": reviewRequests, "latestReviews": latestReviews, "participants": participants,
		"reviewDecision": reviewDecision(pr),
		"commentCount":   pr.Properties.CommentCount, "taskCount": pr.Properties.OpenTaskCount,
	}
	if e.wants("comments") {
		cs, err := c.Comments(r.Project, r.Slug, pr.ID)
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		for _, cm := range cs {
			out = append(out, commentJSON(cm))
		}
		m["comments"] = out
	}
	if e.wants("commits") {
		cs, err := c.PRCommits(r.Project, r.Slug, pr.ID)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, cm := range cs {
			headline, _, _ := strings.Cut(cm.Message, "\n")
			out = append(out, map[string]any{
				"oid": cm.ID, "messageHeadline": headline, "message": cm.Message,
				"author": cm.Author.Name, "authoredDate": ts(cm.AuthorTimestamp),
			})
		}
		m["commits"] = out
	}
	if e.wants("unresolvedThreads") {
		threads, err := buildThreads(c, r, pr)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, t := range threads {
			if t.open() {
				n++
			}
		}
		m["unresolvedThreads"] = n
	}
	if e.wants("files") {
		ch, err := c.Changes(r.Project, r.Slug, pr.ID)
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		for _, f := range ch {
			entry := map[string]any{"path": f.Path.ToString, "changeType": f.Type}
			if f.SrcPath != nil {
				entry["previousPath"] = f.SrcPath.ToString
			}
			out = append(out, entry)
		}
		m["files"] = out
	}
	if e.wants("mergeable") || e.wants("mergeStateStatus") || e.wants("mergeBlockers") {
		ms, err := c.MergeStatus(r.Project, r.Slug, pr.ID)
		switch {
		case err != nil:
			m["mergeable"] = "UNKNOWN"
		case ms.Conflicted:
			m["mergeable"] = "CONFLICTING"
		default:
			m["mergeable"] = "MERGEABLE"
		}
		m["mergeStateStatus"] = mergeStateStatus(pr, ms, err)
		blockers := []string{}
		if err == nil && pr.State == "OPEN" {
			blockers = append(blockers, mergeBlockers(ms)...)
		}
		m["mergeBlockers"] = blockers
	}
	if e.wants("statusCheckRollup") {
		bs, err := c.BuildStatuses(pr.FromRef.LatestCommit)
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		for _, b := range bs {
			out = append(out, map[string]any{"name": b.Name, "key": b.Key, "state": b.State, "url": b.URL, "description": b.Description, "startedAt": ts(b.DateAdded)})
		}
		m["statusCheckRollup"] = out
	}
	return pick(m, e.fields), nil
}

func newPRListCmd(repoFlag *string) *cobra.Command {
	var state, author, reviewer, base, head, search string
	var limit int
	var draft, web bool
	var exp exporter
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List pull requests in a repository",
		Aliases: []string{"ls"},
		Example: `  $ bb pr list --author @me
  $ bb pr list --reviewer @me --state open
  $ bb pr list --json number,title,headRefName --jq '.[] | select(.title | test("fix"))'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			if web {
				repo, err := c.GetRepo(r.Project, r.Slug)
				if err != nil {
					return err
				}
				return openBrowser(strings.TrimSuffix(repo.WebURL(), "/browse") + "/pull-requests")
			}
			bbState := map[string]string{"open": "OPEN", "closed": "DECLINED", "merged": "MERGED", "all": "ALL"}[strings.ToLower(state)]
			if bbState == "" {
				return usageErr("invalid --state %q: valid values are {open|closed|merged|all}", state)
			}
			q := url.Values{"state": {bbState}, "order": {"NEWEST"}}
			if search != "" {
				q.Set("filterText", search)
			}
			if base != "" {
				q.Set("at", "refs/heads/"+base)
				q.Set("direction", "INCOMING")
			} else if head != "" {
				q.Set("at", "refs/heads/"+head)
				q.Set("direction", "OUTGOING")
			}
			n := 1
			for role, who := range map[string]string{"AUTHOR": author, "REVIEWER": reviewer} {
				if who == "" {
					continue
				}
				who, err := meOr(c, who)
				if err != nil {
					return err
				}
				u, err := c.ResolveUser(who)
				if err != nil {
					return err
				}
				q.Set(fmt.Sprintf("username.%d", n), u.Name)
				q.Set(fmt.Sprintf("role.%d", n), role)
				n++
			}
			prs, err := bitbucket.Paginate(c, r.Path()+"/pull-requests", q, limit, func(p bitbucket.PullRequest) bool {
				if head != "" && p.FromRef.DisplayID != head {
					return false
				}
				if base != "" && p.ToRef.DisplayID != base {
					return false
				}
				return !draft || p.Draft
			})
			if err != nil {
				return err
			}
			if exp.enabled() {
				out := []map[string]any{}
				for i := range prs {
					m, err := prJSON(c, r, &prs[i], &exp)
					if err != nil {
						return err
					}
					out = append(out, m)
				}
				return exp.write(stdout, out)
			}
			if len(prs) == 0 {
				return &ExitError{Code: 1, Err: fmt.Errorf("no pull requests match your search in %s", r.FullName())}
			}
			printPRTable(stdout, prs, r)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&state, "state", "s", "open", "Filter by state: {open|closed|merged|all}")
	f.IntVarP(&limit, "limit", "L", 30, "Maximum number of items to fetch")
	f.StringVarP(&author, "author", "A", "", "Filter by author (\"@me\" for yourself)")
	f.StringVar(&reviewer, "reviewer", "", "Filter by reviewer (\"@me\" for yourself)")
	f.StringVarP(&base, "base", "B", "", "Filter by base branch")
	f.StringVarP(&head, "head", "H", "", "Filter by head branch")
	f.StringVarP(&search, "search", "S", "", "Filter by text in title or description")
	f.BoolVarP(&draft, "draft", "d", false, "Filter by draft state")
	f.BoolVarP(&web, "web", "w", false, "List pull requests in the web browser")
	addJSONFlags(cmd, &exp, prFields)
	return cmd
}

func printPRTable(w io.Writer, prs []bitbucket.PullRequest, r Repo) {
	if !isTTY() {
		for _, p := range prs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", p.ID, p.Title, p.FromRef.DisplayID, ghState(&p), ts(p.CreatedDate))
		}
		return
	}
	fmt.Fprintf(w, "\nShowing %d pull requests in %s\n\n", len(prs), r.FullName())
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tBRANCH\tCREATED AT")
	for _, p := range prs {
		id := fmt.Sprintf("#%d", p.ID)
		switch {
		case p.Draft:
			id = gray(id)
		case p.State == "OPEN":
			id = green(id)
		case p.State == "MERGED":
			id = magenta(id)
		default:
			id = red(id)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", id, truncate(p.Title, 60), cyan(p.FromRef.DisplayID), gray(ago(p.CreatedDate)))
	}
	tw.Flush()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func reviewerSummary(pr *bitbucket.PullRequest) string {
	var parts []string
	for _, r := range pr.Reviewers {
		st := map[string]string{"APPROVED": "Approved", "NEEDS_WORK": "Changes requested"}[r.Status]
		if st == "" {
			st = "Requested"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", r.User.Slug, st))
	}
	return strings.Join(parts, ", ")
}

func newPRViewCmd(repoFlag *string) *cobra.Command {
	var comments, web bool
	var exp exporter
	cmd := &cobra.Command{
		Use:   "view [<number> | <url> | <branch>]",
		Short: "View a pull request",
		Long: `Display the title, body, and other information about a pull request.

With --comments, all comment threads are shown including inline comments
(file:line anchors) and comment IDs usable with "bb pr comment --reply-to".`,
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
			if web {
				return openBrowser(pr.WebURL())
			}
			if exp.enabled() {
				m, err := prJSON(c, r, pr, &exp)
				if err != nil {
					return err
				}
				return exp.write(stdout, m)
			}
			var cs []bitbucket.Comment
			if comments {
				if cs, err = c.Comments(r.Project, r.Slug, pr.ID); err != nil {
					return err
				}
			}
			var ms *bitbucket.MergeStatus
			if pr.State == "OPEN" {
				ms, _ = c.MergeStatus(r.Project, r.Slug, pr.ID) // best effort; view works without it
			}
			printPR(stdout, pr, ms, cs, comments)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&comments, "comments", "c", false, "View pull request comments")
	cmd.Flags().BoolVarP(&web, "web", "w", false, "Open a pull request in the browser")
	addJSONFlags(cmd, &exp, prFields)
	return cmd
}

func argOr(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// mergeLine summarises mergeability for pr view; "" when unknown.
func mergeLine(ms *bitbucket.MergeStatus) string {
	if ms == nil {
		return ""
	}
	if ms.CanMerge {
		return "ready"
	}
	return "blocked: " + strings.Join(mergeBlockers(ms), "; ")
}

func printPR(w io.Writer, pr *bitbucket.PullRequest, ms *bitbucket.MergeStatus, cs []bitbucket.Comment, withComments bool) {
	if !isTTY() {
		fmt.Fprintf(w, "title:\t%s\n", pr.Title)
		fmt.Fprintf(w, "state:\t%s\n", ghState(pr))
		fmt.Fprintf(w, "author:\t%s\n", pr.Author.User.Slug)
		fmt.Fprintf(w, "reviewers:\t%s\n", reviewerSummary(pr))
		fmt.Fprintf(w, "number:\t%d\n", pr.ID)
		fmt.Fprintf(w, "url:\t%s\n", pr.WebURL())
		fmt.Fprintf(w, "head:\t%s\n", pr.FromRef.DisplayID)
		fmt.Fprintf(w, "base:\t%s\n", pr.ToRef.DisplayID)
		fmt.Fprintf(w, "draft:\t%t\n", pr.Draft)
		fmt.Fprintf(w, "comments:\t%d\n", pr.Properties.CommentCount)
		fmt.Fprintf(w, "open tasks:\t%d\n", pr.Properties.OpenTaskCount)
		if ml := mergeLine(ms); ml != "" {
			fmt.Fprintf(w, "merge:\t%s\n", ml)
		}
		fmt.Fprintln(w, "--")
		fmt.Fprintln(w, pr.Description)
		if withComments {
			fmt.Fprintln(w, "--")
			printComments(w, cs)
		}
		return
	}
	state := ghState(pr)
	switch {
	case pr.Draft:
		state = gray("Draft")
	case state == "OPEN":
		state = green("Open")
	case state == "MERGED":
		state = magenta("Merged")
	default:
		state = red("Closed")
	}
	fmt.Fprintf(w, "%s %s\n", bold(pr.Title), gray(fmt.Sprintf("#%d", pr.ID)))
	fmt.Fprintf(w, "%s • %s wants to merge into %s from %s • %s\n", state, pr.Author.User.Slug, cyan(pr.ToRef.DisplayID), cyan(pr.FromRef.DisplayID), gray(ago(pr.CreatedDate)))
	if rs := reviewerSummary(pr); rs != "" {
		fmt.Fprintf(w, "%s %s\n", bold("Reviewers:"), rs)
	}
	fmt.Fprintf(w, "%s %d comments, %d open tasks\n", bold("Activity:"), pr.Properties.CommentCount, pr.Properties.OpenTaskCount)
	if ms != nil {
		if ms.CanMerge {
			fmt.Fprintf(w, "%s %s\n", bold("Merge:"), green("✓ Ready to merge"))
		} else {
			fmt.Fprintf(w, "%s %s\n", bold("Merge:"), red("✗ Blocked"))
			for _, b := range mergeBlockers(ms) {
				fmt.Fprintf(w, "  - %s\n", b)
			}
		}
	}
	fmt.Fprintln(w)
	if strings.TrimSpace(pr.Description) == "" {
		fmt.Fprintln(w, gray("No description provided"))
	} else {
		fmt.Fprintln(w, pr.Description)
	}
	if withComments {
		fmt.Fprintln(w, "\n"+bold("Comments"))
		printComments(w, cs)
	}
	fmt.Fprintln(w, gray("\nView this pull request on Bitbucket: "+pr.WebURL()))
}

func printComments(w io.Writer, cs []bitbucket.Comment) {
	if len(cs) == 0 {
		fmt.Fprintln(w, "(no comments)")
	}
	for _, cm := range cs {
		printComment(w, cm, 0)
		fmt.Fprintln(w)
	}
}

func printComment(w io.Writer, cm bitbucket.Comment, depth int) {
	indent := strings.Repeat("  ", depth)
	head := []string{bold(cm.Author.Slug), ts(cm.CreatedDate), fmt.Sprintf("id:%d", cm.ID)}
	if a := cm.Anchor; a != nil && a.Path != "" {
		loc := a.Path
		if a.Line > 0 {
			loc += fmt.Sprintf(":%d (%s)", a.Line, a.LineType)
		}
		if a.Orphaned {
			loc += " [outdated]"
		}
		head = append(head, cyan(loc))
	}
	if cm.Severity == "BLOCKER" {
		head = append(head, yellow("[task "+strings.ToLower(cm.State)+"]"))
	}
	if cm.ThreadResolved {
		head = append(head, gray("[resolved]"))
	}
	prefix := ""
	if depth > 0 {
		prefix = "↳ "
	}
	fmt.Fprintf(w, "%s%s%s\n", indent, prefix, strings.Join(head, " • "))
	for _, line := range strings.Split(cm.Text, "\n") {
		fmt.Fprintf(w, "%s  %s\n", indent, line)
	}
	for _, r := range cm.Comments {
		printComment(w, r, depth+1)
	}
}

func newPRStatusCmd(repoFlag *string) *cobra.Command {
	var exp exporter
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show status of relevant pull requests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			me, err := c.CurrentUser()
			if err != nil {
				return err
			}
			var current *bitbucket.PullRequest
			if r.Remote != "" {
				current, _ = resolvePR(c, &r, "")
			}
			byRole := func(role string) ([]bitbucket.PullRequest, error) {
				u, err := c.ResolveUser(me)
				if err != nil {
					return nil, err
				}
				q := url.Values{"state": {"OPEN"}, "role.1": {role}, "username.1": {u.Name}}
				return bitbucket.Paginate[bitbucket.PullRequest](c, r.Path()+"/pull-requests", q, 30, nil)
			}
			created, err := byRole("AUTHOR")
			if err != nil {
				return err
			}
			review, err := byRole("REVIEWER")
			if err != nil {
				return err
			}
			if exp.enabled() {
				conv := func(prs []bitbucket.PullRequest) ([]map[string]any, error) {
					out := []map[string]any{}
					for i := range prs {
						m, err := prJSON(c, r, &prs[i], &exp)
						if err != nil {
							return nil, err
						}
						out = append(out, m)
					}
					return out, nil
				}
				res := map[string]any{"currentBranch": nil}
				if current != nil {
					if res["currentBranch"], err = prJSON(c, r, current, &exp); err != nil {
						return err
					}
				}
				if res["createdBy"], err = conv(created); err != nil {
					return err
				}
				if res["needsReview"], err = conv(review); err != nil {
					return err
				}
				return exp.write(stdout, res)
			}
			fmt.Fprintf(stdout, "\nRelevant pull requests in %s\n\n", r.FullName())
			section := func(title string, prs []bitbucket.PullRequest) {
				fmt.Fprintln(stdout, bold(title))
				if len(prs) == 0 {
					fmt.Fprintln(stdout, gray("  You have no pull requests here"))
				}
				for _, p := range prs {
					fmt.Fprintf(stdout, "  %s  %s [%s]\n", green(fmt.Sprintf("#%d", p.ID)), p.Title, cyan(p.FromRef.DisplayID))
					if d := reviewDecision(&p); d != "" {
						fmt.Fprintf(stdout, "    - %s\n", strings.ReplaceAll(strings.ToLower(d), "_", " "))
					}
				}
				fmt.Fprintln(stdout)
			}
			var cur []bitbucket.PullRequest
			if current != nil {
				cur = append(cur, *current)
			}
			section("Current branch", cur)
			section("Created by you", created)
			section("Requesting a code review from you", review)
			return nil
		},
	}
	addJSONFlags(cmd, &exp, prFields)
	return cmd
}

package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/git"
)

func newPRDiffCmd(repoFlag *string) *cobra.Command {
	var nameOnly, patch, web, lineNumbers, asJSON, stat, sinceLastReview bool
	var color, since string
	var exclude []string
	var maxLines, contextLines int
	cmd := &cobra.Command{
		Use:   "diff [<number> | <url> | <branch>] [-- <path>...]",
		Short: "View changes in a pull request",
		Long: `View changes in a pull request.

Agent-oriented options:
  -n, --line-numbers     print explicit OLD/NEW line numbers; use them verbatim
                         for "bb pr comment --line" and review files
  --json                 structured diff: files → hunks → lines with old/new numbers
  --stat                 per-file additions/deletions
  -- <path>...           only these paths (directory prefixes or globs, ** allowed)
  --exclude <glob>       skip paths, e.g. --exclude 'vendor/**,*.lock'
  --max-lines N          stop before exceeding N lines; omitted files are listed on stderr
  --since-last-review    only changes since the commit you last reviewed
  --since <commit>       only changes since <commit>

Line-numbered and JSON output use the server's default context, which is
exactly the set of lines Bitbucket accepts comments on.`,
		Example: `  $ bb pr diff 42 --stat
  $ bb pr diff 42 -n -- src/api/
  $ bb pr diff 42 -n --exclude '**/*_test.go' --max-lines 1500
  $ bb pr diff 42 --since-last-review -n`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var paths []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				args, paths = args[:dash], args[dash:]
			}
			if len(args) > 1 {
				return usageErr("accepts at most one pull request argument; put paths after \"--\"")
			}
			if color != "auto" && color != "always" && color != "never" {
				return usageErr("invalid --color %q: valid values are {always|never|auto}", color)
			}
			if sinceLastReview && since != "" {
				return usageErr("--since and --since-last-review are mutually exclusive")
			}
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, argOr(args))
			if err != nil {
				return err
			}
			if web {
				return openBrowser(pr.WebURL() + "/diff")
			}
			filter := newPathFilter(paths, exclude)
			if sinceLastReview {
				if since, err = lastReviewedCommit(c, pr); err != nil {
					return err
				}
			}
			head := pr.FromRef.LatestCommit
			if since != "" {
				if strings.HasPrefix(head, since) {
					fmt.Fprintf(stderr, "No changes since %s (head is %s).\n", short(since), short(head))
					return nil
				}
				fmt.Fprintf(stderr, "Changes from %s to %s (head).\n", short(since), short(head))
			}

			if nameOnly && since == "" {
				ch, err := c.Changes(r.Project, r.Slug, pr.ID)
				if err != nil {
					return err
				}
				for _, f := range ch {
					var old string
					if f.SrcPath != nil {
						old = f.SrcPath.ToString
					}
					if filter.match(f.Path.ToString, old) {
						fmt.Fprintln(stdout, f.Path.ToString)
					}
				}
				return nil
			}

			structured := lineNumbers || asJSON || stat || nameOnly || since != ""
			if !structured {
				d, err := c.RawDiff(r.Project, r.Slug, pr.ID, patch)
				if err != nil {
					return err
				}
				var secs []rawSection
				for _, s := range splitRawDiff(d) {
					if s.path == "" || filter.match(s.path, s.oldPath) {
						secs = append(secs, s)
					}
				}
				return printSections(secs, "", color, maxLines)
			}

			ctx := contextLines
			if stat || nameOnly {
				ctx = 0
			}
			var d *bitbucket.DiffResponse
			if since != "" {
				d, err = c.CompareDiff(r.Project, r.Slug, head, since, "", ctx)
				if bitbucket.IsNotFound(err) {
					return fmt.Errorf("commit %s not found (rebased or force-pushed?); review the full diff instead", short(since))
				}
			} else {
				d, err = c.PRDiff(r.Project, r.Slug, pr.ID, "", ctx)
			}
			if err != nil {
				return err
			}
			files := filter.diffs(d.Diffs)
			if d.Truncated {
				fmt.Fprintln(stderr, "warning: the server truncated this diff; narrow it with paths after \"--\"")
			}
			switch {
			case nameOnly:
				for _, f := range files {
					fmt.Fprintln(stdout, f.Path())
				}
			case stat:
				return printStat(files)
			case asJSON:
				out := []map[string]any{}
				var omitted []string
				used := 0
				for _, f := range files {
					m := diffJSON(f)
					n := 0
					for _, h := range f.Hunks {
						for _, s := range h.Segments {
							n += len(s.Lines)
						}
					}
					if maxLines > 0 && used+n > maxLines && used > 0 {
						omitted = append(omitted, f.Path())
						continue
					}
					used += n
					out = append(out, m)
				}
				reportOmitted(omitted)
				return (&exporter{}).write(stdout, map[string]any{
					"headRefOid": head, "since": since, "truncated": d.Truncated, "files": out,
				})
			case lineNumbers:
				var secs []rawSection
				for _, f := range files {
					secs = append(secs, rawSection{path: f.Path(), text: numbered(f)})
				}
				return printSections(secs, numberedLegend, "never", maxLines)
			default:
				var secs []rawSection
				for _, f := range files {
					secs = append(secs, rawSection{path: f.Path(), text: (&bitbucket.DiffResponse{Diffs: []bitbucket.Diff{f}}).Unified()})
				}
				return printSections(secs, "", color, maxLines)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&nameOnly, "name-only", false, "Display only names of changed files")
	f.BoolVar(&patch, "patch", false, "Display diff in patch format")
	f.BoolVarP(&web, "web", "w", false, "Open the pull request diff in the browser")
	f.StringVar(&color, "color", "auto", "Use color in diff output: {always|never|auto}")
	f.BoolVarP(&lineNumbers, "line-numbers", "n", false, "Show explicit old/new line numbers for commenting")
	f.BoolVar(&asJSON, "json", false, "Output the diff as structured JSON")
	f.BoolVar(&stat, "stat", false, "Show per-file additions and deletions")
	f.StringSliceVar(&exclude, "exclude", nil, "Skip paths matching these `globs`")
	f.IntVar(&maxLines, "max-lines", 0, "Stop output before exceeding this many lines (0: unlimited)")
	f.IntVarP(&contextLines, "context", "U", -1, "Context lines for structured output (default: server default)")
	f.BoolVar(&sinceLastReview, "since-last-review", false, "Only show changes since the commit you last reviewed")
	f.StringVar(&since, "since", "", "Only show changes since this `commit`")
	return cmd
}

// printSections writes file sections, stopping before maxLines is exceeded.
func printSections(secs []rawSection, legend, color string, maxLines int) error {
	out, omitted := limitSections(secs, maxLines)
	if out == "" && len(omitted) == 0 {
		fmt.Fprintln(stderr, "No changes match the given paths.")
		return nil
	}
	if color == "always" || color == "auto" && colorOn() {
		out = colorizeDiff(out)
	}
	fmt.Fprint(stdout, legend+out)
	reportOmitted(omitted)
	return nil
}

func reportOmitted(omitted []string) {
	if len(omitted) > 0 {
		fmt.Fprintf(stderr, "\n# --max-lines reached; %d file(s) not shown (request them with `-- <path>`):\n#   %s\n", len(omitted), strings.Join(omitted, "\n#   "))
	}
}

func printStat(files []bitbucket.Diff) error {
	ta, td := 0, 0
	for _, f := range files {
		a, d := diffStat(f)
		ta, td = ta+a, td+d
		path := f.Path()
		if f.ChangeType() == "RENAME" {
			path = f.OldPath() + " → " + path
		}
		fmt.Fprintf(stdout, "%6s %6s  %s\n", fmt.Sprintf("+%d", a), fmt.Sprintf("-%d", d), path)
	}
	fmt.Fprintf(stdout, "%d files changed, +%d -%d\n", len(files), ta, td)
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// lastReviewedCommit returns the commit the current user last reviewed.
func lastReviewedCommit(c *bitbucket.Client, pr *bitbucket.PullRequest) (string, error) {
	me, err := c.CurrentUser()
	if err != nil {
		return "", err
	}
	for _, p := range append(pr.Reviewers, pr.Participants...) {
		if p.User.Slug == me && p.LastReviewedCommit != "" {
			return p.LastReviewedCommit, nil
		}
	}
	return "", fmt.Errorf("no previous review by %s recorded on #%d; use --since <commit> or review the full diff", me, pr.ID)
}

func colorizeDiff(d string) string {
	lines := strings.SplitAfter(d, "\n")
	for i, l := range lines {
		code := ""
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff "):
			code = "1"
		case strings.HasPrefix(l, "@@"):
			code = "36"
		case strings.HasPrefix(l, "+"):
			code = "32"
		case strings.HasPrefix(l, "-"):
			code = "31"
		}
		if code != "" {
			body := strings.TrimSuffix(l, "\n")
			lines[i] = "\x1b[" + code + "m" + body + "\x1b[0m" + l[len(body):]
		}
	}
	return strings.Join(lines, "")
}

func newPRChecksCmd(repoFlag *string) *cobra.Command {
	var web, watch bool
	var interval int
	var exp exporter
	cmd := &cobra.Command{
		Use:   "checks [<number> | <url> | <branch>]",
		Short: "Show build status for a pull request",
		Long: `Show build statuses reported for the pull request's head commit.

With --watch, wait until no check is pending (re-reading the head commit,
so it follows new pushes). If no check has appeared after 2 minutes, it
gives up.

Exit codes: 0 all passed, 1 a check failed, 8 checks pending.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			arg := argOr(args)
			pr, err := resolvePR(c, &r, arg)
			if err != nil {
				return err
			}
			if web {
				return openBrowser(pr.WebURL() + "/builds")
			}
			if arg == "" {
				arg = strconv.Itoa(pr.ID) // keep following the same PR while watching
			}
			var bs []bitbucket.BuildStatus
			for waited := 0; ; waited += interval {
				if bs, err = c.BuildStatuses(pr.FromRef.LatestCommit); err != nil {
					return err
				}
				var pending []string
				for _, b := range bs {
					if bucket(b.State) == "pending" {
						pending = append(pending, firstNonEmpty(b.Name, b.Key))
					}
				}
				if !watch || (len(bs) > 0 && len(pending) == 0) || (len(bs) == 0 && waited >= 120) {
					break
				}
				if len(bs) == 0 {
					fmt.Fprintf(stderr, "waiting for checks on %s…\n", short(pr.FromRef.LatestCommit))
				} else {
					fmt.Fprintf(stderr, "waiting: %d pending (%s)…\n", len(pending), strings.Join(pending, ", "))
				}
				sleep(time.Duration(interval) * time.Second)
				if pr, err = resolvePR(c, &r, arg); err != nil {
					return err
				}
			}
			if exp.enabled() {
				out := []map[string]any{}
				for _, b := range bs {
					out = append(out, pick(map[string]any{
						"name": b.Name, "key": b.Key, "state": b.State, "bucket": bucket(b.State),
						"link": b.URL, "description": b.Description, "startedAt": ts(b.DateAdded),
					}, exp.fields))
				}
				if err := exp.write(stdout, out); err != nil {
					return err
				}
			} else if len(bs) == 0 {
				return fmt.Errorf("no checks reported on the '%s' branch", pr.FromRef.DisplayID)
			} else {
				printChecks(bs)
			}
			failed, pending := false, false
			for _, b := range bs {
				switch bucket(b.State) {
				case "fail":
					failed = true
				case "pending":
					pending = true
				}
			}
			switch {
			case failed:
				return &ExitError{Code: 1}
			case pending:
				return &ExitError{Code: 8}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&web, "web", "w", false, "Open the web browser to show build details")
	cmd.Flags().BoolVar(&watch, "watch", false, "Wait until no check is pending")
	cmd.Flags().IntVarP(&interval, "interval", "i", 10, "Refresh interval in seconds for --watch")
	addJSONFlags(cmd, &exp, []string{"bucket", "description", "key", "link", "name", "startedAt", "state"})
	return cmd
}

func bucket(state string) string {
	switch state {
	case "SUCCESSFUL":
		return "pass"
	case "FAILED":
		return "fail"
	case "CANCELLED":
		return "cancel"
	}
	return "pending"
}

func printChecks(bs []bitbucket.BuildStatus) {
	if !isTTY() {
		for _, b := range bs {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", firstNonEmpty(b.Name, b.Key), bucket(b.State), b.URL, b.Description)
		}
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	for _, b := range bs {
		mark := map[string]string{"pass": green("✓"), "fail": red("X"), "cancel": gray("-")}[bucket(b.State)]
		if mark == "" {
			mark = yellow("*")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, firstNonEmpty(b.Name, b.Key), gray(b.Description), b.URL)
	}
	tw.Flush()
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func newPRCheckoutCmd(repoFlag *string) *cobra.Command {
	var branch string
	var force bool
	cmd := &cobra.Command{
		Use:     "checkout {<number> | <url> | <branch>}",
		Short:   "Check out a pull request in git",
		Aliases: []string{"co"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(*repoFlag)
			if err != nil {
				return err
			}
			pr, err := resolvePR(c, &r, args[0])
			if err != nil {
				return err
			}
			remote := r.Remote
			if remote == "" {
				repo, err := c.GetRepo(r.Project, r.Slug)
				if err != nil {
					return err
				}
				if remote = repo.CloneURL("ssh"); remote == "" {
					remote = repo.CloneURL("http")
				}
			}
			local := firstNonEmpty(branch, pr.FromRef.DisplayID)
			var src string
			if pr.IsCrossRepository() || r.Remote == "" {
				src = fmt.Sprintf("refs/pull-requests/%d/from", pr.ID)
			} else {
				src = "refs/heads/" + pr.FromRef.DisplayID
			}
			tracking := fmt.Sprintf("refs/remotes/%s/%s", r.Remote, pr.FromRef.DisplayID)
			if r.Remote == "" || pr.IsCrossRepository() {
				tracking = "refs/heads/" + local
				if cur, _ := git.CurrentBranch(); cur == local {
					tracking = ""
				}
			}
			fetchArgs := []string{"fetch", remote}
			if tracking != "" {
				refspec := src + ":" + tracking
				if force || strings.HasPrefix(tracking, "refs/remotes/") {
					refspec = "+" + refspec
				}
				fetchArgs = append(fetchArgs, refspec)
			} else {
				fetchArgs = append(fetchArgs, src)
			}
			if err := git.Passthrough(fetchArgs...); err != nil {
				return err
			}
			switch {
			case tracking == "":
				return git.Passthrough("merge", "--ff-only", "FETCH_HEAD")
			case strings.HasPrefix(tracking, "refs/heads/"):
				return git.Passthrough("checkout", local)
			case git.HasLocalBranch(local):
				if err := git.Passthrough("checkout", local); err != nil {
					return err
				}
				if force {
					return git.Passthrough("reset", "--hard", tracking)
				}
				return git.Passthrough("merge", "--ff-only", tracking)
			}
			return git.Passthrough("checkout", "-b", local, "--track", r.Remote+"/"+pr.FromRef.DisplayID)
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Local branch name to use (default [the name of the head branch])")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Reset the existing local branch to the latest state of the pull request")
	return cmd
}

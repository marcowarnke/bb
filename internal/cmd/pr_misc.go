package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/git"
)

func newPRDiffCmd(repoFlag *string) *cobra.Command {
	var nameOnly, patch, web bool
	var color string
	cmd := &cobra.Command{
		Use:   "diff [<number> | <url> | <branch>]",
		Short: "View changes in a pull request",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if color != "auto" && color != "always" && color != "never" {
				return usageErr("invalid --color %q: valid values are {always|never|auto}", color)
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
			if nameOnly {
				ch, err := c.Changes(r.Project, r.Slug, pr.ID)
				if err != nil {
					return err
				}
				for _, f := range ch {
					fmt.Fprintln(stdout, f.Path.ToString)
				}
				return nil
			}
			d, err := c.RawDiff(r.Project, r.Slug, pr.ID, patch)
			if err != nil {
				return err
			}
			if color == "always" || color == "auto" && colorOn() {
				d = colorizeDiff(d)
			}
			fmt.Fprint(stdout, d)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&nameOnly, "name-only", false, "Display only names of changed files")
	f.BoolVar(&patch, "patch", false, "Display diff in patch format")
	f.BoolVarP(&web, "web", "w", false, "Open the pull request diff in the browser")
	f.StringVar(&color, "color", "auto", "Use color in diff output: {always|never|auto}")
	return cmd
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
	var web bool
	var exp exporter
	cmd := &cobra.Command{
		Use:   "checks [<number> | <url> | <branch>]",
		Short: "Show build status for a pull request",
		Long: `Show build statuses reported for the pull request's head commit.

Exit codes: 0 all passed, 1 a check failed, 8 checks pending.`,
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
				return openBrowser(pr.WebURL() + "/builds")
			}
			bs, err := c.BuildStatuses(pr.FromRef.LatestCommit)
			if err != nil {
				return err
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

package cmd

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/config"
	"github.com/marcowarnke/bb/internal/git"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func newBrowseCmd() *cobra.Command {
	var repoFlag, branch, commit string
	var noBrowser, settings, prs bool
	cmd := &cobra.Command{
		Use:   "browse [<number> | <path>[:<line>[-<line>]] | <commit>]",
		Short: "Open the repository in the browser",
		Long: `Open the Bitbucket repository in the web browser.

The argument can be a pull request number, a commit SHA, or a file or
directory path (relative to the current directory) with an optional line or
line range, e.g. "src/main.go:42" or "src/main.go:10-20".

Files are shown on --branch or --commit if given; otherwise on the current
branch if it has been pushed (has an upstream), else on the default branch.`,
		Example: `  $ bb browse
  $ bb browse 42
  $ bb browse src/main.go:42
  $ bb browse src/ --branch release/1.0
  $ bb browse --commit          # last commit
  $ bb browse -n src/main.go    # print the URL instead`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("branch") && cmd.Flags().Changed("commit") {
				return usageErr("specify only one of --branch or --commit")
			}
			r, err := resolveRepo(repoFlag)
			if err != nil {
				return err
			}
			base, err := repoWebURL(r)
			if err != nil {
				return err
			}
			if commit == "last" {
				if commit, err = git.Run("rev-parse", "HEAD"); err != nil {
					return err
				}
			}
			u, err := browseURL(base, r, argOr(args), branch, commit, settings, prs)
			if err != nil {
				return err
			}
			if noBrowser {
				fmt.Fprintln(stdout, u)
				return nil
			}
			return openBrowser(u)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&repoFlag, "repo", "R", "", "Select another repository using the `[HOST/]PROJECT/REPO` format")
	f.StringVarP(&branch, "branch", "b", "", "Select another branch by passing in the branch name")
	f.StringVarP(&commit, "commit", "c", "", "Select another commit by passing in the commit SHA, default is the last commit")
	f.Lookup("commit").NoOptDefVal = "last"
	f.BoolVarP(&noBrowser, "no-browser", "n", false, "Print destination URL instead of opening the browser")
	f.BoolVarP(&settings, "settings", "s", false, "Open repository settings")
	f.BoolVarP(&prs, "pull-requests", "p", false, "Open the repository's pull requests")
	return cmd
}

// repoWebURL builds the repository's web URL from the configured host; no API
// call or token needed.
func repoWebURL(r Repo) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	_, h, err := cfg.Resolve(r.Host)
	base := ""
	switch {
	case h != nil && h.BaseURL != "":
		base = h.BaseURL // a missing token doesn't matter here
	case r.Host != "":
		base = "https://" + r.Host
	default:
		return "", err
	}
	if user, ok := strings.CutPrefix(r.Project, "~"); ok {
		return base + "/users/" + url.PathEscape(strings.ToLower(user)) + "/repos/" + url.PathEscape(r.Slug), nil
	}
	return base + "/projects/" + url.PathEscape(r.Project) + "/repos/" + url.PathEscape(r.Slug), nil
}

func browseURL(base string, r Repo, arg, branch, commit string, settings, prs bool) (string, error) {
	switch {
	case settings:
		return base + "/settings", nil
	case prs:
		return base + "/pull-requests", nil
	}
	at := ""
	switch {
	case commit != "":
		at = commit
	case branch != "":
		at = "refs/heads/" + branch
	}
	if arg == "" {
		if commit != "" {
			return base + "/commits/" + url.PathEscape(commit), nil
		}
		return withAt(base+"/browse", at, ""), nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil && n > 0 {
		return base + "/pull-requests/" + strconv.Itoa(n), nil
	}
	if shaRE.MatchString(arg) {
		if _, err := os.Stat(arg); err != nil {
			return base + "/commits/" + arg, nil
		}
	}
	p, lines, err := splitPathLine(arg)
	if err != nil {
		return "", err
	}
	if r.Remote != "" {
		// Paths are relative to the current directory inside the checkout.
		if prefix, err := git.Run("rev-parse", "--show-prefix"); err == nil {
			p = path.Join(prefix, p)
		}
		if at == "" {
			at = pushedBranchRef()
		}
	}
	p = strings.Trim(path.Clean("/"+p), "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			segs = append(segs, url.PathEscape(s))
		}
	}
	return withAt(base+"/browse/"+strings.Join(segs, "/"), at, lines), nil
}

func withAt(u, at, fragment string) string {
	if at != "" {
		u += "?at=" + url.QueryEscape(at)
	}
	if fragment != "" {
		u += "#" + fragment
	}
	return u
}

// splitPathLine splits "file:42" or "file:10-20" into the path and the
// Bitbucket line fragment ("42", "10-20").
func splitPathLine(arg string) (string, string, error) {
	i := strings.LastIndex(arg, ":")
	if i < 0 {
		return arg, "", nil
	}
	p, spec := arg[:i], arg[i+1:]
	from, to, isRange := strings.Cut(spec, "-")
	a, err := strconv.Atoi(from)
	if err != nil || a < 1 {
		return "", "", fmt.Errorf("invalid line number %q in %q", spec, arg)
	}
	if !isRange {
		return p, from, nil
	}
	b, err := strconv.Atoi(to)
	if err != nil || b < a {
		return "", "", fmt.Errorf("invalid line range %q in %q", spec, arg)
	}
	return p, from + "-" + to, nil
}

// pushedBranchRef returns the current branch's upstream ref on the remote,
// or "" (default branch) when the branch hasn't been pushed.
func pushedBranchRef() string {
	up, err := git.Run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return ""
	}
	if _, b, ok := strings.Cut(up, "/"); ok {
		return "refs/heads/" + b
	}
	return ""
}

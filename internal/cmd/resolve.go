package cmd

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/config"
	"github.com/marcowarnke/bb/internal/git"
)

// Repo identifies a repository on a host. Remote is the matching git remote
// in the current checkout, if any.
type Repo struct {
	Host    string
	Project string
	Slug    string
	Remote  string
}

func (r Repo) FullName() string { return r.Project + "/" + r.Slug }
func (r Repo) Path() string     { return bitbucket.RepoPath(r.Project, r.Slug) }
func (r Repo) PRPath(id int) string {
	return bitbucket.PRPath(r.Project, r.Slug, id)
}

// parseRepoArg accepts PROJECT/REPO, HOST/PROJECT/REPO or a URL.
func parseRepoArg(s string) (Repo, error) {
	if strings.Contains(s, "://") || strings.Contains(s, "@") {
		host, p, slug, ok := git.ParseRemoteURL(s)
		if !ok {
			return Repo{}, fmt.Errorf("could not parse repository URL %q", s)
		}
		return Repo{Host: host, Project: p, Slug: slug}, nil
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	switch len(parts) {
	case 2:
		return Repo{Project: git.NormalizeProject(parts[0]), Slug: strings.ToLower(parts[1])}, nil
	case 3:
		return Repo{Host: parts[0], Project: git.NormalizeProject(parts[1]), Slug: strings.ToLower(parts[2])}, nil
	}
	return Repo{}, fmt.Errorf("expected the \"[HOST/]PROJECT/REPO\" format, got %q", s)
}

// resolveRepo determines the target repo from -R, BB_REPO or git remotes.
func resolveRepo(flag string) (Repo, error) {
	if flag == "" {
		flag = os.Getenv("BB_REPO")
	}
	cfg, err := config.Load()
	if err != nil {
		return Repo{}, err
	}
	remotes, _ := git.Remotes()
	if flag != "" {
		r, err := parseRepoArg(flag)
		if err != nil {
			return Repo{}, err
		}
		for _, rm := range remotes {
			if _, p, s, ok := git.ParseRemoteURL(rm.URL); ok && p == r.Project && s == r.Slug {
				r.Remote = rm.Name
				break
			}
		}
		return r, nil
	}
	if len(remotes) == 0 {
		return Repo{}, fmt.Errorf("not in a git repository with a Bitbucket remote; use -R PROJECT/REPO")
	}
	var fallback *Repo
	for _, rm := range remotes {
		host, p, s, ok := git.ParseRemoteURL(rm.URL)
		if !ok {
			continue
		}
		r := Repo{Host: host, Project: p, Slug: s, Remote: rm.Name}
		if _, known := cfg.Hosts[host]; known {
			return r, nil
		}
		if fallback == nil {
			fallback = &r
		}
	}
	if fallback == nil {
		return Repo{}, fmt.Errorf("none of the git remotes point to a Bitbucket repository; use -R PROJECT/REPO")
	}
	// SSH hostnames often differ from the web host; let the config decide.
	if len(cfg.Hosts) == 1 || os.Getenv("BB_HOST") != "" {
		fallback.Host = ""
	}
	return *fallback, nil
}

func clientFor(host string) (*bitbucket.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	_, h, err := cfg.Resolve(host)
	if err != nil {
		return nil, &ExitError{Code: 4, Err: err}
	}
	c := bitbucket.New(h.BaseURL, h.Token)
	if os.Getenv("BB_TOKEN") == "" {
		c.UserSlug = h.User
	}
	return c, nil
}

// repoClient resolves both the repo and an API client for it.
func repoClient(repoFlag string) (Repo, *bitbucket.Client, error) {
	r, err := resolveRepo(repoFlag)
	if err != nil {
		return r, nil, err
	}
	c, err := clientFor(r.Host)
	return r, c, err
}

// resolvePR finds a PR from a number, URL, branch name, or the current branch.
func resolvePR(c *bitbucket.Client, r *Repo, arg string) (*bitbucket.PullRequest, error) {
	if arg != "" {
		if n, err := strconv.Atoi(strings.TrimPrefix(arg, "#")); err == nil {
			return c.GetPR(r.Project, r.Slug, n)
		}
		if strings.Contains(arg, "://") {
			u, err := url.Parse(arg)
			if err == nil {
				if p, s, n, ok := parsePRURL(u.Path); ok {
					r.Project, r.Slug = p, s
					return c.GetPR(p, s, n)
				}
			}
			return nil, fmt.Errorf("invalid pull request URL %q", arg)
		}
	} else {
		b, err := git.CurrentBranch()
		if err != nil {
			return nil, err
		}
		arg = b
	}
	for _, state := range []string{"OPEN", "ALL"} {
		q := url.Values{"state": {state}, "direction": {"OUTGOING"}, "at": {"refs/heads/" + arg}, "order": {"NEWEST"}}
		prs, err := bitbucket.Paginate(c, r.Path()+"/pull-requests", q, 1, func(p bitbucket.PullRequest) bool {
			return p.FromRef.DisplayID == arg
		})
		if err != nil {
			return nil, err
		}
		if len(prs) > 0 {
			return &prs[0], nil
		}
	}
	return nil, fmt.Errorf("no pull requests found for branch %q", arg)
}

// parsePRURL handles /projects/P/repos/r/pull-requests/N[/...] and /users/u/repos/...
func parsePRURL(path string) (project, slug string, id int, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i+4 < len(parts); i++ {
		if (parts[i] == "projects" || parts[i] == "users") && parts[i+2] == "repos" && parts[i+4] == "pull-requests" && i+5 < len(parts) {
			n, err := strconv.Atoi(parts[i+5])
			if err != nil {
				return
			}
			project = parts[i+1]
			if parts[i] == "users" {
				project = "~" + project
			}
			return git.NormalizeProject(project), parts[i+3], n, true
		}
	}
	return
}

// meOr replaces "@me" with the current user's slug.
func meOr(c *bitbucket.Client, v string) (string, error) {
	if v == "@me" {
		return c.CurrentUser()
	}
	return v, nil
}

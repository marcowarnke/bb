// Package git wraps the git CLI and parses Bitbucket remote URLs.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

func Run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Passthrough runs git with stdio attached to the terminal.
func Passthrough(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	return cmd.Run()
}

func CurrentBranch() (string, error) {
	b, err := Run("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", errors.New("could not determine current branch (detached HEAD or not a git repository)")
	}
	return b, nil
}

func HasLocalBranch(name string) bool {
	_, err := Run("rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

type Remote struct {
	Name string
	URL  string
}

func Remotes() ([]Remote, error) {
	out, err := Run("remote", "-v")
	if err != nil {
		return nil, err
	}
	var rs []Remote
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "(fetch)" || seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		rs = append(rs, Remote{Name: f[0], URL: f[1]})
	}
	// Prefer upstream, then origin, like gh.
	rank := func(n string) int {
		switch n {
		case "upstream":
			return 0
		case "origin":
			return 1
		}
		return 2
	}
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rank(rs[j].Name) < rank(rs[j-1].Name); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	return rs, nil
}

// ParseRemoteURL extracts host, project key and repo slug from a Bitbucket
// Data Center clone or browse URL, e.g.
//
//	https://bitbucket.corp/scm/proj/repo.git
//	https://bitbucket.corp/ctx/scm/~user/repo.git
//	ssh://git@bitbucket.corp:7999/proj/repo.git
//	git@bitbucket.corp:proj/repo.git
//	https://bitbucket.corp/projects/PROJ/repos/repo/browse
func ParseRemoteURL(raw string) (host, project, slug string, ok bool) {
	if !strings.Contains(raw, "://") {
		// scp-like syntax: user@host:path
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if colon < 0 || colon < at {
			return "", "", "", false
		}
		raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", false
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	for i, p := range parts {
		if p == "scm" && i+2 < len(parts) {
			project, slug = parts[i+1], parts[i+2]
			break
		}
		if (p == "projects" || p == "users") && i+3 < len(parts) && parts[i+2] == "repos" {
			project, slug = parts[i+1], parts[i+3]
			if p == "users" {
				project = "~" + project
			}
			break
		}
	}
	if project == "" && len(parts) >= 2 {
		project, slug = parts[len(parts)-2], parts[len(parts)-1]
	}
	slug = strings.TrimSuffix(slug, ".git")
	if project == "" || slug == "" {
		return "", "", "", false
	}
	return u.Hostname(), NormalizeProject(project), strings.ToLower(slug), true
}

// NormalizeProject upper-cases project keys; personal projects (~user) keep case.
func NormalizeProject(p string) string {
	if strings.HasPrefix(p, "~") {
		return p
	}
	return strings.ToUpper(p)
}

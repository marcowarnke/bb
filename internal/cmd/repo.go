package cmd

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/config"
	"github.com/marcowarnke/bb/internal/git"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "repo <command>", Short: "Manage repositories"}
	cmd.AddCommand(newRepoViewCmd(), newRepoCloneCmd())
	return cmd
}

func newRepoViewCmd() *cobra.Command {
	var web bool
	var exp exporter
	cmd := &cobra.Command{
		Use:   "view [<repository>]",
		Short: "View a repository",
		Long:  "Display the description and the README of a Bitbucket repository.\n\nWith no argument, the repository for the current directory is displayed.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, c, err := repoClient(argOr(args))
			if err != nil {
				return err
			}
			repo, err := c.GetRepo(r.Project, r.Slug)
			if err != nil {
				return err
			}
			if web {
				return openBrowser(repo.WebURL())
			}
			def, _ := c.DefaultBranch(r.Project, r.Slug)
			if exp.enabled() {
				var parent any
				if repo.Origin != nil {
					parent = repo.Origin.FullName()
				}
				return exp.write(stdout, pick(map[string]any{
					"id": repo.ID, "name": repo.Name, "slug": repo.Slug, "project": repo.Project.Key,
					"nameWithOwner": repo.FullName(), "description": repo.Description, "url": repo.WebURL(),
					"sshUrl": repo.CloneURL("ssh"), "httpUrl": repo.CloneURL("http"),
					"defaultBranchRef": map[string]any{"name": def}, "isPrivate": !repo.Public,
					"isFork": repo.Origin != nil, "parent": parent,
				}, exp.fields))
			}
			fmt.Fprintf(stdout, "name:\t%s\n", repo.FullName())
			fmt.Fprintf(stdout, "description:\t%s\n", repo.Description)
			fmt.Fprintf(stdout, "default branch:\t%s\n", def)
			fmt.Fprintf(stdout, "url:\t%s\n", repo.WebURL())
			fmt.Fprintf(stdout, "clone:\t%s %s\n", repo.CloneURL("ssh"), repo.CloneURL("http"))
			if readme := fetchReadme(c, r); readme != "" {
				fmt.Fprintln(stdout, "--")
				fmt.Fprintln(stdout, readme)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&web, "web", "w", false, "Open a repository in the browser")
	addJSONFlags(cmd, &exp, []string{"defaultBranchRef", "description", "httpUrl", "id", "isFork", "isPrivate", "name", "nameWithOwner", "parent", "project", "slug", "sshUrl", "url"})
	return cmd
}

func fetchReadme(c *bitbucket.Client, r Repo) string {
	for _, name := range []string{"README.md", "README", "README.txt", "readme.md"} {
		resp, err := c.Raw("GET", r.Path()+"/raw/"+name, nil, http.Header{"Accept": {"text/plain"}}, nil)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return strings.TrimRight(string(data), "\n")
	}
	return ""
}

func newRepoCloneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone <repository> [<directory>] [-- <gitflags>...]",
		Short: "Clone a repository locally",
		Long: `Clone a Bitbucket repository locally. Pass additional git clone flags after "--".

The protocol follows the host's git_protocol setting (bb auth login -p ssh|https).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := parseRepoArg(args[0])
			if err != nil {
				return err
			}
			c, err := clientFor(r.Host)
			if err != nil {
				return err
			}
			repo, err := c.GetRepo(r.Project, r.Slug)
			if err != nil {
				return err
			}
			proto := "http"
			if cfg, err := config.Load(); err == nil {
				if _, h, err := cfg.Resolve(r.Host); err == nil && h.GitProtocol == "ssh" {
					proto = "ssh"
				}
			}
			u := repo.CloneURL(proto)
			if u == "" {
				return fmt.Errorf("no %s clone URL for %s", proto, repo.FullName())
			}
			gitArgs := append([]string{"clone", u}, args[1:]...)
			return git.Passthrough(gitArgs...)
		},
	}
	return cmd
}

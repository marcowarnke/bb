package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/marcowarnke/bb/internal/bitbucket"
)

// Dynamic shell completion. Shells show the text after "\t" as a
// description (zsh, fish, powershell, bash v2), so PR numbers come with their
// titles. Every completer fails silently: a slow or unreachable server must
// never break the shell.

const completionTimeout = 5 * time.Second

type completer func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective)

var (
	userFlags   = map[string]bool{"reviewer": true, "author": true, "add-reviewer": true, "remove-reviewer": true}
	branchFlags = map[string]bool{"base": true, "head": true, "branch": true}
)

// registerCompletions wires completers into the command tree by argument
// shape and flag name, so new commands pick them up automatically.
func registerCompletions(c *cobra.Command) {
	if strings.Contains(c.Use, "<number>") && c.ValidArgsFunction == nil {
		state := "OPEN"
		if c.Name() == "reopen" {
			state = "DECLINED"
		}
		c.ValidArgsFunction = completePRs(state, c.Name() == "browse" || c.Name() == "diff")
	}
	register := func(f *pflag.Flag) {
		var fn completer
		switch {
		case userFlags[f.Name]:
			fn = completeUsers
		case branchFlags[f.Name]:
			fn = completeBranches
		case f.Name == "repo":
			fn = completeRepos
		case f.Name == "state" && c.Name() == "list":
			fn = cobra.FixedCompletions([]string{"open", "closed", "merged", "all"}, cobra.ShellCompDirectiveNoFileComp)
		}
		if fn != nil {
			_ = c.RegisterFlagCompletionFunc(f.Name, fn)
		}
	}
	c.LocalNonPersistentFlags().VisitAll(register)
	c.PersistentFlags().VisitAll(register)
	for _, sub := range c.Commands() {
		registerCompletions(sub)
	}
}

func completionClient(cmd *cobra.Command) (Repo, *bitbucket.Client, error) {
	repoFlag := ""
	if f := cmd.Flag("repo"); f != nil {
		repoFlag = f.Value.String()
	}
	r, c, err := repoClient(repoFlag)
	if err == nil {
		c.HTTP.Timeout = completionTimeout
	}
	return r, c, err
}

// completePRs offers PR numbers, newest first, described as
// "title · author · branch". files: also fall back to file completion
// (browse paths, diff paths after the PR).
func completePRs(state string, files bool) completer {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		fileDir := cobra.ShellCompDirectiveNoFileComp
		if files {
			fileDir = cobra.ShellCompDirectiveDefault
		}
		if len(args) > 0 || (toComplete != "" && strings.TrimLeft(toComplete, "#0123456789") != "") {
			return nil, fileDir
		}
		r, c, err := completionClient(cmd)
		if err != nil {
			return nil, fileDir
		}
		prs, err := bitbucket.Paginate[bitbucket.PullRequest](c, r.Path()+"/pull-requests", url.Values{"state": {state}, "order": {"NEWEST"}}, 100, nil)
		if err != nil {
			return nil, fileDir
		}
		prefix := strings.TrimPrefix(toComplete, "#")
		var out []string
		for _, p := range prs {
			n := strconv.Itoa(p.ID)
			if !strings.HasPrefix(n, prefix) {
				continue
			}
			title := p.Title
			if p.Draft {
				title = "[draft] " + title
			}
			out = append(out, fmt.Sprintf("%s\t%s · %s · %s", n, oneLine(title), p.Author.User.Slug, p.FromRef.DisplayID))
		}
		dir := cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
		if files && len(out) == 0 {
			dir = cobra.ShellCompDirectiveDefault
		}
		return out, dir
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(truncate(s, 80)), " ")
}

// lastItem splits comma-separated flag values: only the last item is completed.
func lastItem(toComplete string) (head, item string) {
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		return toComplete[:i+1], toComplete[i+1:]
	}
	return "", toComplete
}

func completeUsers(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	head, item := lastItem(toComplete)
	var out []string
	if strings.HasPrefix("@me", item) && cmd.Name() == "list" {
		out = append(out, head+"@me\tyourself")
	}
	_, c, err := completionClient(cmd)
	if err != nil {
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	users, err := bitbucket.Paginate[bitbucket.User](c, "rest/api/1.0/users", url.Values{"filter": {item}}, 25, nil)
	if err != nil {
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	for _, u := range users {
		out = append(out, head+u.Slug+"\t"+u.DisplayName)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeBranches(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	r, c, err := completionClient(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	refs, err := bitbucket.Paginate[bitbucket.Ref](c, r.Path()+"/branches", url.Values{"filterText": {toComplete}, "orderBy": {"MODIFICATION"}}, 50, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, ref := range refs {
		out = append(out, ref.DisplayID)
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}

// completeRepos completes "PROJECT/" first, then that project's repositories.
// Candidates keep the typed casing because shells filter them by prefix.
func completeRepos(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, err := clientFor("")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c.HTTP.Timeout = completionTimeout
	project, name, hasSlash := strings.Cut(toComplete, "/")
	var out []string
	if !hasSlash {
		projects, err := bitbucket.Paginate[bitbucket.Project](c, "rest/api/1.0/projects", nil, 0, nil)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		for _, p := range projects {
			if strings.HasPrefix(strings.ToUpper(p.Key), strings.ToUpper(toComplete)) {
				out = append(out, toComplete+p.Key[len(toComplete):]+"/\t"+oneLine(p.Name))
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
	repos, err := bitbucket.Paginate[bitbucket.Repository](c, "rest/api/1.0/projects/"+url.PathEscape(strings.ToUpper(project))+"/repos", nil, 0, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	for _, r := range repos {
		if strings.HasPrefix(r.Slug, strings.ToLower(name)) {
			out = append(out, project+"/"+r.Slug+"\t"+oneLine(r.Name))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

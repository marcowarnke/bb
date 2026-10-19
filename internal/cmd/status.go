package cmd

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/bitbucket"
)

// statusSection is one block of `bb status`: a dashboard query.
type statusSection struct {
	key, title, empty   string
	role, participation string
}

var statusSections = []statusSection{
	{"authored", "Your pull requests", "You have no open pull requests", "AUTHOR", ""},
	{"reviewRequested", "Review requested", "Nothing to review", "REVIEWER", "UNAPPROVED"},
	{"waitingOnAuthor", "Waiting on author (you requested changes)", "None", "REVIEWER", "NEEDS_WORK"},
}

func newStatusCmd() *cobra.Command {
	var hostname string
	var limit int
	var exp exporter
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print your pull request dashboard across all repositories",
		Long: `Show open pull requests relevant to you across every repository on the host:

- Your pull requests, with whether each can be merged and what blocks it
  (approvals, builds, tasks, conflicts).
- Pull requests waiting for your review.
- Pull requests where you requested changes, waiting on the author.

With --json, the result is an object with "authored", "reviewRequested" and
"waitingOnAuthor" arrays of pull requests (same fields as "bb pr view", plus
"repository").`,
		Example: `  $ bb status
  $ bb status --json number,title,repository,url --jq '.reviewRequested[].url'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := clientFor(hostname)
			if err != nil {
				return err
			}
			results := make([][]bitbucket.PullRequest, len(statusSections))
			for i, s := range statusSections {
				q := url.Values{"state": {"OPEN"}, "role": {s.role}, "order": {"NEWEST"}}
				if s.participation != "" {
					q.Set("participantStatus", s.participation)
				}
				if results[i], err = bitbucket.Paginate[bitbucket.PullRequest](c, "rest/api/1.0/dashboard/pull-requests", q, limit, nil); err != nil {
					return err
				}
			}
			if exp.enabled() {
				res := map[string]any{}
				for i, s := range statusSections {
					out := []map[string]any{}
					for j := range results[i] {
						pr := &results[i][j]
						m, err := prJSON(c, prRepo(pr), pr, &exp)
						if err != nil {
							return err
						}
						out = append(out, m)
					}
					res[s.key] = out
				}
				return exp.write(stdout, res)
			}
			merge := mergeStatuses(c, results[0])
			for i, s := range statusSections {
				printStatusSection(s, results[i], merge)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "The Bitbucket `host` to use (default: BB_HOST or the only configured host)")
	cmd.Flags().IntVarP(&limit, "limit", "L", 30, "Maximum number of pull requests per section")
	addJSONFlags(cmd, &exp, prFields)
	return cmd
}

// prRepo returns the repository a PR targets (dashboard PRs span repos).
func prRepo(pr *bitbucket.PullRequest) Repo {
	if tr := pr.ToRef.Repository; tr != nil {
		return Repo{Project: tr.Project.Key, Slug: tr.Slug}
	}
	return Repo{}
}

// mergeStatuses fetches merge status for each PR concurrently; failures are
// left out (shown as unknown).
func mergeStatuses(c *bitbucket.Client, prs []bitbucket.PullRequest) map[int]*bitbucket.MergeStatus {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	out := map[int]*bitbucket.MergeStatus{}
	for i := range prs {
		wg.Add(1)
		go func(pr *bitbucket.PullRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := prRepo(pr)
			if ms, err := c.MergeStatus(r.Project, r.Slug, pr.ID); err == nil {
				mu.Lock()
				out[i] = ms
				mu.Unlock()
			}
		}(&prs[i])
	}
	wg.Wait()
	return out
}

// statusNote is the per-PR detail: merge readiness for your PRs, author and
// age for others'.
func statusNote(s statusSection, i int, pr *bitbucket.PullRequest, merge map[int]*bitbucket.MergeStatus) string {
	if s.role != "AUTHOR" {
		return fmt.Sprintf("by %s, updated %s", pr.Author.User.Slug, ago(pr.UpdatedDate))
	}
	ms, ok := merge[i]
	switch {
	case !ok:
		return "merge status unknown"
	case ms.CanMerge && pr.Draft:
		return "draft; ready to merge once published"
	case ms.CanMerge:
		return "ready to merge"
	}
	return "blocked: " + strings.Join(mergeBlockers(ms), "; ")
}

func printStatusSection(s statusSection, prs []bitbucket.PullRequest, merge map[int]*bitbucket.MergeStatus) {
	if !isTTY() {
		for i := range prs {
			pr := &prs[i]
			fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\t%s\t%s\n", s.key, prRepo(pr).FullName(), pr.ID, pr.Title, pr.WebURL(), statusNote(s, i, pr, merge))
		}
		return
	}
	fmt.Fprintln(stdout, bold(s.title))
	if len(prs) == 0 {
		fmt.Fprintln(stdout, gray("  "+s.empty))
	}
	for i := range prs {
		pr := &prs[i]
		ref := fmt.Sprintf("%s#%d", prRepo(pr).FullName(), pr.ID)
		if pr.Draft {
			ref = gray(ref)
		} else {
			ref = green(ref)
		}
		fmt.Fprintf(stdout, "  %s  %s\n", ref, pr.Title)
		note := statusNote(s, i, pr, merge)
		switch {
		case strings.HasPrefix(note, "ready"):
			note = green("✓ " + note)
		case strings.HasPrefix(note, "blocked"):
			note = red("✗ ") + note
		default:
			note = gray(note)
		}
		fmt.Fprintf(stdout, "    %s\n", note)
	}
	fmt.Fprintln(stdout)
}

package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/git"
	"github.com/marcowarnke/bb/skills"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills <command>",
		Short: "Manage the Claude Code skills shipped with bb",
		Long: `bb embeds Claude Code skills that teach agents to use it:

  bb                   command reference and conventions
  bb-review            review a pull request end to end
  bb-address-feedback  work through review feedback on your own PRs

They always match this binary's commands and flags.`,
	}
	cmd.AddCommand(newSkillsListCmd(), newSkillsInstallCmd())
	return cmd
}

func skillNames() []string {
	entries, _ := fs.ReadDir(skills.FS, ".")
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the embedded skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, n := range skillNames() {
				data, _ := fs.ReadFile(skills.FS, n+"/SKILL.md")
				fmt.Fprintf(stdout, "%s\t%s\n", n, frontmatter(data, "description"))
			}
			return nil
		},
	}
}

func frontmatter(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func newSkillsInstallCmd() *cobra.Command {
	var project bool
	var dir string
	cmd := &cobra.Command{
		Use:   "install [<skill>...]",
		Short: "Install the skills for Claude Code",
		Long: `Write the embedded skills to ~/.claude/skills (all projects), to the current
repository's .claude/skills with --project, or to --dir. Without arguments all
skills are installed. Existing copies are overwritten, so re-run this after
upgrading bb.`,
		Example: `  $ bb skills install
  $ bb skills install --project bb bb-review`,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return skillNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if project && dir != "" {
				return usageErr("specify only one of --project or --dir")
			}
			switch {
			case project:
				top, err := git.Run("rev-parse", "--show-toplevel")
				if err != nil {
					return fmt.Errorf("--project needs a git repository: %w", err)
				}
				dir = filepath.Join(top, ".claude", "skills")
			case dir == "":
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				dir = filepath.Join(home, ".claude", "skills")
			}
			names := args
			if len(names) == 0 {
				names = skillNames()
			}
			for _, n := range names {
				data, err := fs.ReadFile(skills.FS, n+"/SKILL.md")
				if err != nil {
					return fmt.Errorf("unknown skill %q; available: %s", n, strings.Join(skillNames(), ", "))
				}
				dst := filepath.Join(dir, n, "SKILL.md")
				old, err := os.ReadFile(dst)
				verb := "Installed"
				switch {
				case err == nil && bytes.Equal(old, data):
					success("%s is up to date (%s)", n, dst)
					continue
				case err == nil:
					verb = "Updated"
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					return err
				}
				success("%s %s (%s)", verb, n, dst)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Install into the current repository's .claude/skills")
	cmd.Flags().StringVar(&dir, "dir", "", "Install into `directory` instead")
	return cmd
}

package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/marcowarnke/bb/internal/git"
)

// Every `bb ...` invocation in the shipped skills must use real subcommands
// and flags, so the agent instructions can't drift from the CLI.
func TestSkillsReferenceRealFlags(t *testing.T) {
	files, _ := filepath.Glob("../../skills/*/SKILL.md")
	if len(files) < 3 {
		t.Fatalf("expected skill files, found %v", files)
	}
	invocation := regexp.MustCompile("bb ((?:pr|repo|auth|api|browse|status|skills)\\b[^`|\\n]*)")
	flag := regexp.MustCompile(`(?:^|\s)(--?[a-zA-Z][\w-]*)`)
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range invocation.FindAllStringSubmatch(string(data), -1) {
			line := m[1]
			words := strings.Fields(line)
			cmd, _, err := NewRoot().Find(words)
			if err != nil || cmd == nil || cmd.Name() == "bb" {
				t.Errorf("%s: unknown command in %q", filepath.Base(filepath.Dir(file)), "bb "+line)
				continue
			}
			for _, f := range flag.FindAllStringSubmatch(line, -1) {
				name := f[1]
				if !hasFlag(cmd, name) {
					t.Errorf("%s: %q has no flag %s (in %q)", filepath.Base(filepath.Dir(file)), cmd.CommandPath(), name, "bb "+line)
				}
				checked++
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d flags checked; regexp broken?", checked)
	}
}

func hasFlag(c *cobra.Command, name string) bool {
	if name == "--" || name == "-" {
		return true
	}
	fs := c.Flags()
	fs.AddFlagSet(c.InheritedFlags())
	if strings.HasPrefix(name, "--") {
		return fs.Lookup(strings.TrimPrefix(name, "--")) != nil
	}
	return len(name) == 2 && fs.ShorthandLookup(name[1:]) != nil
}

func TestSkillsInstall(t *testing.T) {
	dir := t.TempDir()
	_, errOut, code := run(t, "", "skills", "install", "--dir", dir)
	if code != 0 || strings.Count(errOut, "Installed") != 3 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	want, _ := os.ReadFile("../../skills/bb-review/SKILL.md")
	got, _ := os.ReadFile(filepath.Join(dir, "bb-review", "SKILL.md"))
	if string(got) != string(want) {
		t.Fatal("installed skill differs from source")
	}
	os.WriteFile(filepath.Join(dir, "bb", "SKILL.md"), []byte("stale"), 0o644)
	_, errOut, _ = run(t, "", "skills", "install", "--dir", dir, "bb", "bb-review")
	if !strings.Contains(errOut, "Updated bb") || !strings.Contains(errOut, "bb-review is up to date") {
		t.Fatalf("err=%q", errOut)
	}
	if _, errOut, code = run(t, "", "skills", "install", "--dir", dir, "nope"); code == 0 || !strings.Contains(errOut, "available: bb, bb-address-feedback, bb-review") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	out, _, _ := run(t, "", "skills", "list")
	if !strings.HasPrefix(out, "bb\tBitbucket Data Center CLI") {
		t.Fatalf("list=%q", out)
	}
}

// sandbox points HOME at a temp dir and chdirs into a fresh git repo.
func sandbox(t *testing.T) (home, repo string) {
	home, repo = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(repo)
	if _, err := git.Run("init", "-q"); err != nil {
		t.Fatal(err)
	}
	repo, _ = git.Run("rev-parse", "--show-toplevel") // resolves /private/var symlinks on macOS
	return home, repo
}

func installed(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "bb-review", "SKILL.md"))
	return err == nil
}

func TestSkillsInstallTargets(t *testing.T) {
	home, repo := sandbox(t)
	// Piped, no flags: unchanged default.
	if _, errOut, code := run(t, "", "skills", "install"); code != 0 || !installed(t, filepath.Join(home, ".claude", "skills")) {
		t.Fatalf("default: code=%d err=%q", code, errOut)
	}
	_, errOut, code := run(t, "", "skills", "install", "--agent", "copilot,agents", "--scope", "user,project")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, d := range []string{
		filepath.Join(home, ".copilot", "skills"), filepath.Join(home, ".agents", "skills"),
		filepath.Join(repo, ".github", "skills"), filepath.Join(repo, ".agents", "skills"),
	} {
		if !installed(t, d) {
			t.Errorf("not installed in %s", d)
		}
	}
	if installed(t, filepath.Join(repo, ".claude", "skills")) {
		t.Error("claude project dir written without being asked")
	}
	if _, errOut, code = run(t, "", "skills", "install", "--project"); code != 0 || !installed(t, filepath.Join(repo, ".claude", "skills")) {
		t.Fatalf("--project: code=%d err=%q", code, errOut)
	}
	if _, errOut, code = run(t, "", "skills", "install", "--agent", "cursor"); code == 0 || !strings.Contains(errOut, "invalid --agent") {
		t.Fatalf("err=%q", errOut)
	}
	if _, errOut, code = run(t, "", "skills", "install", "--dir", "x", "--project"); code == 0 || !strings.Contains(errOut, "can't be combined") {
		t.Fatalf("err=%q", errOut)
	}
	t.Chdir(home)
	if _, errOut, code = run(t, "", "skills", "install", "--scope", "project"); code == 0 || !strings.Contains(errOut, "needs a git repository") {
		t.Fatalf("err=%q", errOut)
	}
}

func TestSkillsInstallPrompt(t *testing.T) {
	home, repo := sandbox(t)
	os.MkdirAll(filepath.Join(home, ".claude", "skills", "bb"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "skills", "bb", "SKILL.md"), []byte("old"), 0o644)
	_, errOut, code := runTTY(t, "2, 5\n", "skills", "install")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, want := range []string{
		"1. Claude Code, personal", "~/.claude/skills (outdated)",
		"2. GitHub Copilot, personal", "~/.copilot/skills (not installed)",
		"5. GitHub Copilot, this repository", "Choose one or more",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("menu lacks %q:\n%s", want, errOut)
		}
	}
	if !installed(t, filepath.Join(home, ".copilot", "skills")) || !installed(t, filepath.Join(repo, ".github", "skills")) {
		t.Fatalf("choices not installed:\n%s", errOut)
	}
	if installed(t, filepath.Join(home, ".claude", "skills")) {
		t.Fatal("unchosen target written")
	}
	// Enter = 1 (Claude, personal).
	if _, _, code = runTTY(t, "\n", "skills", "install"); code != 0 || !installed(t, filepath.Join(home, ".claude", "skills")) {
		t.Fatal("default choice not installed")
	}
	if _, errOut, code = runTTY(t, "9\n", "skills", "install"); code == 0 || !strings.Contains(errOut, "between 1 and 6") {
		t.Fatalf("err=%q", errOut)
	}
	if _, errOut, code = runTTY(t, "", "skills", "install"); code == 0 || !strings.Contains(errOut, "no choice made") {
		t.Fatalf("EOF must not install: err=%q", errOut)
	}
}

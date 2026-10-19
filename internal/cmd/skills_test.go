package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every `bb ...` invocation in the shipped skills must use real subcommands
// and flags, so the agent instructions can't drift from the CLI.
func TestSkillsReferenceRealFlags(t *testing.T) {
	files, _ := filepath.Glob("../../skills/*/SKILL.md")
	if len(files) < 3 {
		t.Fatalf("expected skill files, found %v", files)
	}
	invocation := regexp.MustCompile("bb ((?:pr|repo|auth|api|browse)\\b[^`|\\n]*)")
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

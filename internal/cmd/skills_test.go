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
	invocation := regexp.MustCompile("bb ((?:pr|repo|auth|api)\\b[^`|\\n]*)")
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

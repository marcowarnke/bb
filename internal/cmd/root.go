// Package cmd implements the bb command tree, mirroring gh's UX.
package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/config"
)

var Version = "dev"

// IO is swappable for tests.
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
	isTTY            = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }
	sleep            = time.Sleep
)

// ExitError carries a specific exit code (gh: 1 error, 2 cancel, 4 auth, 8 pending checks).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "bb <command> <subcommand> [flags]",
		Short: "Bitbucket Data Center CLI",
		Long: `Work seamlessly with Bitbucket Data Center from the command line.

Modelled after the GitHub CLI (gh): same commands, flags and output conventions.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Example: `  $ bb pr list
  $ bb pr create --fill --reviewer alice
  $ bb pr diff 42
  $ bb pr comment 42 --body "LGTM" --path src/main.go --line 10
  $ bb pr review 42 --approve`,
	}
	root.SetFlagErrorFunc(flagError)
	root.AddCommand(newAuthCmd(), newBrowseCmd(), newPRCmd(), newRepoCmd(), newAPICmd(), newSkillsCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := NewRoot()
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Err != nil {
			fmt.Fprintln(stderr, ee.Err)
		}
		return ee.Code
	}
	fmt.Fprintln(stderr, err)
	var api *bitbucket.APIError
	if errors.As(err, &api) && api.StatusCode == 401 || errors.Is(err, config.ErrNoHost) {
		return 4
	}
	return 1
}

// flagError mirrors gh: `--json` without a value lists the available fields.
func flagError(c *cobra.Command, err error) error {
	if strings.Contains(err.Error(), "flag needs an argument: --json") {
		if f := c.Flags().Lookup("json"); f != nil {
			if fields, ok := f.Annotations["fields"]; ok {
				return fmt.Errorf("Specify one or more comma-separated fields for `--json`:\n  %s", strings.Join(fields, "\n  "))
			}
		}
	}
	return err
}

func usageErr(format string, a ...any) error {
	return fmt.Errorf(format, a...)
}

// readBody resolves --body / --body-file ("-" means stdin).
func readBody(body, bodyFile string, flags *pflag.FlagSet) (string, bool, error) {
	if flags.Changed("body-file") {
		var data []byte
		var err error
		if bodyFile == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(bodyFile)
		}
		return strings.TrimRight(string(data), "\n"), true, err
	}
	return body, flags.Changed("body"), nil
}

func success(format string, a ...any) {
	mark := "✓"
	if isTTY() {
		mark = green(mark)
	}
	fmt.Fprintf(stderr, mark+" "+format+"\n", a...)
}

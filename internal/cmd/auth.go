package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/marcowarnke/bb/internal/bitbucket"
	"github.com/marcowarnke/bb/internal/config"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <command>",
		Short: "Authenticate bb with a Bitbucket Data Center host",
		Long: `Authenticate with a Bitbucket Data Center host using an HTTP access token
(Profile → Manage account → HTTP access tokens). Tokens need at least
repository write permission to create and comment on pull requests.

Alternatively set BB_HOST and BB_TOKEN; they take precedence over stored credentials.`,
	}
	for _, sub := range []*cobra.Command{newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd(), newAuthTokenCmd()} {
		// -h is --hostname here (as in gh); declaring --help ourselves stops
		// cobra from adding its -h shorthand, which would panic.
		sub.Flags().Bool("help", false, "Show help for command")
		cmd.AddCommand(sub)
	}
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	var hostname, gitProtocol string
	var withToken, insecureStorage bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to a Bitbucket host",
		Long: `Log in to a Bitbucket Data Center host with an HTTP access token.

The token is stored in the OS credential store (macOS Keychain, Windows
Credential Manager, Linux Secret Service). If none is available, or with
--insecure-storage, it is stored in plain text in the config file.`,
		Example: `  $ bb auth login --hostname bitbucket.example.com
  $ echo "$TOKEN" | bb auth login --hostname https://example.com/bitbucket --with-token`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostname == "" {
				hostname = os.Getenv("BB_HOST")
			}
			if hostname == "" {
				if !isTTY() {
					return usageErr("--hostname required when not running interactively")
				}
				hostname = prompt("Bitbucket host (e.g. bitbucket.example.com): ")
			}
			key, base, err := config.NormalizeHost(hostname)
			if err != nil {
				return err
			}
			var token string
			switch {
			case withToken:
				data, err := io.ReadAll(stdin)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(data))
			case isTTY():
				fmt.Fprintf(stderr, "Create a token at %s/plugins/servlet/access-tokens/users/manage\n", base)
				fmt.Fprint(stderr, "Paste your HTTP access token: ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(stderr)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			default:
				return usageErr("--with-token required when not running interactively")
			}
			if token == "" {
				return errors.New("no token provided")
			}
			u, err := bitbucket.New(base, token).Whoami()
			if err != nil {
				return fmt.Errorf("validating token against %s: %w", base, err)
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if old, ok := cfg.Hosts[key]; ok && old.User != "" {
				_ = config.KeyringDelete(key, old.User)
			}
			h := &config.Host{BaseURL: base, User: u.Slug, GitProtocol: gitProtocol}
			storage := "keyring"
			if !insecureStorage {
				if err := config.KeyringSet(key, u.Slug, token); err != nil {
					fmt.Fprintf(stderr, "warning: could not store token in the OS keyring (%v); storing it in plain text in %s\n", err, config.Path())
					insecureStorage = true
				}
			}
			if insecureStorage {
				h.Token, storage = token, config.Path()
			}
			cfg.Hosts[key] = h
			if err := cfg.Save(); err != nil {
				return err
			}
			success("Logged in to %s as %s (token stored in %s)", key, bold(u.Slug), storage)
			return nil
		},
	}
	cmd.Flags().StringVarP(&hostname, "hostname", "h", "", "Bitbucket host or base URL (include the context path if any)")
	cmd.Flags().BoolVar(&withToken, "with-token", false, "Read token from standard input")
	cmd.Flags().BoolVar(&insecureStorage, "insecure-storage", false, "Save the token in plain text instead of the OS credential store")
	cmd.Flags().StringVarP(&gitProtocol, "git-protocol", "p", "https", "Protocol for git operations: {ssh|https}")
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	var hostname string
	var showToken bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display authentication state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			names := cfg.HostNames()
			if hostname != "" {
				names = []string{hostname}
			} else if h := os.Getenv("BB_HOST"); h != "" {
				k, _, _ := config.NormalizeHost(h)
				if !contains(names, k) {
					names = append(names, h)
				}
			}
			if len(names) == 0 {
				return &ExitError{Code: 1, Err: config.ErrNoHost}
			}
			failed := false
			for _, n := range names {
				key, h, err := cfg.Resolve(n)
				fmt.Fprintln(stdout, bold(key))
				if err != nil {
					fmt.Fprintf(stdout, "  %s %v\n", red("X"), err)
					failed = true
					continue
				}
				source := h.TokenSource
				u, err := bitbucket.New(h.BaseURL, h.Token).Whoami()
				if err != nil {
					fmt.Fprintf(stdout, "  %s Failed to log in to %s using token (%s): %v\n", red("X"), key, source, err)
					failed = true
					continue
				}
				fmt.Fprintf(stdout, "  %s Logged in to %s as %s (%s)\n", green("✓"), key, bold(u.Slug), source)
				fmt.Fprintf(stdout, "  - API base URL: %s\n", h.BaseURL)
				if h.GitProtocol != "" {
					fmt.Fprintf(stdout, "  - Git operations protocol: %s\n", h.GitProtocol)
				}
				tok := h.Token
				if !showToken && len(tok) > 4 {
					tok = tok[:4] + strings.Repeat("*", 12)
				}
				fmt.Fprintf(stdout, "  - Token: %s\n", tok)
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&hostname, "hostname", "h", "", "Check only a specific host")
	cmd.Flags().BoolVarP(&showToken, "show-token", "t", false, "Display the auth token")
	return cmd
}

func newAuthLogoutCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Log out of a Bitbucket host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if hostname == "" {
				if len(cfg.Hosts) != 1 {
					return usageErr("--hostname required (logged in to %d hosts)", len(cfg.Hosts))
				}
				hostname = cfg.HostNames()[0]
			}
			key, _, err := config.NormalizeHost(hostname)
			if err != nil {
				return err
			}
			h, ok := cfg.Hosts[key]
			if !ok {
				return fmt.Errorf("not logged in to %s", key)
			}
			if err := config.KeyringDelete(key, h.User); err != nil {
				return fmt.Errorf("removing token from the OS keyring: %w", err)
			}
			delete(cfg.Hosts, key)
			if err := cfg.Save(); err != nil {
				return err
			}
			success("Logged out of %s account %s", key, h.User)
			return nil
		},
	}
	cmd.Flags().StringVarP(&hostname, "hostname", "h", "", "The host to log out of")
	return cmd
}

func newAuthTokenCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the auth token bb is configured to use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			_, h, err := cfg.Resolve(hostname)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, h.Token)
			return nil
		},
	}
	cmd.Flags().StringVarP(&hostname, "hostname", "h", "", "The host to print the token for")
	return cmd
}

func prompt(label string) string {
	fmt.Fprint(stderr, label)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

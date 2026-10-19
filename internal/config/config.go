// Package config stores per-host credentials, modelled after gh's hosts.yml.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Host struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token,omitempty"`
	User        string `json:"user,omitempty"`
	GitProtocol string `json:"git_protocol,omitempty"`
}

type Config struct {
	Hosts map[string]*Host `json:"hosts"`
	path  string
}

func Dir() string {
	if d := os.Getenv("BB_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "bb")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bb")
}

func Path() string { return filepath.Join(Dir(), "config.json") }

func Load() (*Config, error) {
	c := &Config{Hosts: map[string]*Host{}, path: Path()}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", c.path, err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]*Host{}
	}
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, append(data, '\n'), 0o600)
}

func (c *Config) HostNames() []string {
	var names []string
	for n := range c.Hosts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// NormalizeHost turns "bitbucket.corp", "https://bitbucket.corp/ctx/" etc. into
// a host key ("bitbucket.corp") and base URL ("https://bitbucket.corp/ctx").
func NormalizeHost(in string) (key, baseURL string, err error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", "", errors.New("empty hostname")
	}
	if !strings.Contains(in, "://") {
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("invalid hostname %q", in)
	}
	return u.Hostname(), strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// Resolve picks the host to use. Precedence: explicit name, BB_HOST, the only
// configured host. BB_TOKEN overrides the stored token.
func (c *Config) Resolve(name string) (string, *Host, error) {
	if name == "" {
		name = os.Getenv("BB_HOST")
	}
	var key string
	var h *Host
	if name != "" {
		k, base, err := NormalizeHost(name)
		if err != nil {
			return "", nil, err
		}
		key = k
		if stored, ok := c.Hosts[k]; ok {
			cp := *stored
			h = &cp
		} else {
			h = &Host{BaseURL: base}
		}
	} else {
		switch len(c.Hosts) {
		case 0:
			return "", nil, ErrNoHost
		case 1:
			for k, v := range c.Hosts {
				cp := *v
				key, h = k, &cp
			}
		default:
			return "", nil, fmt.Errorf("multiple hosts configured (%s); set BB_HOST or pass --hostname/-R HOST/PROJECT/REPO", strings.Join(c.HostNames(), ", "))
		}
	}
	if t := os.Getenv("BB_TOKEN"); t != "" {
		h.Token = t
	}
	if h.Token == "" {
		return key, h, fmt.Errorf("not logged in to %s: run `bb auth login --hostname %s` or set BB_TOKEN", key, key)
	}
	return key, h, nil
}

var ErrNoHost = errors.New("no Bitbucket host configured: run `bb auth login --hostname <host>` or set BB_HOST and BB_TOKEN")

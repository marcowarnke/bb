package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/marcowarnke/bb/internal/config"
)

func configFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAuthLoginKeyring(t *testing.T) {
	f := newFake(t)
	t.Setenv("BB_TOKEN", "")
	t.Setenv("BB_HOST", "")

	_, errOut, code := run(t, "secret\n", "auth", "login", "--hostname", f.srv.URL, "--with-token")
	if code != 0 || !strings.Contains(errOut, "token stored in keyring") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if strings.Contains(configFile(t), "secret") {
		t.Fatal("token leaked into config file")
	}
	if tok, _ := keyring.Get("bb:127.0.0.1", "me"); tok != "secret" {
		t.Fatalf("keyring token=%q", tok)
	}

	// Subsequent commands read the token back from the keyring.
	out, errOut, code := run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--json", "number", "--jq", ".number")
	if code != 0 || strings.TrimSpace(out) != "7" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	out, _, _ = run(t, "", "auth", "status")
	if !strings.Contains(out, "as me (keyring)") {
		t.Fatalf("status=%q", out)
	}

	_, errOut, code = run(t, "", "auth", "logout")
	if code != 0 {
		t.Fatal(errOut)
	}
	if _, err := keyring.Get("bb:127.0.0.1", "me"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("token still in keyring after logout: %v", err)
	}
}

func TestAuthLoginInsecureStorage(t *testing.T) {
	f := newFake(t)
	t.Setenv("BB_TOKEN", "")

	_, errOut, code := run(t, "secret", "auth", "login", "--hostname", f.srv.URL, "--with-token", "--insecure-storage")
	if code != 0 {
		t.Fatal(errOut)
	}
	if !strings.Contains(configFile(t), `"token": "secret"`) {
		t.Fatalf("expected plain-text token, config=%s", configFile(t))
	}
	if _, err := keyring.Get("bb:127.0.0.1", "me"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("token should not be in keyring")
	}
}

func TestAuthLoginKeyringUnavailable(t *testing.T) {
	f := newFake(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	t.Setenv("BB_TOKEN", "")

	_, errOut, code := run(t, "secret", "auth", "login", "--hostname", f.srv.URL, "--with-token")
	if code != 0 || !strings.Contains(errOut, "storing it in plain text") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(configFile(t), `"token": "secret"`) {
		t.Fatalf("expected fallback to file, config=%s", configFile(t))
	}
	// The file token works even though the keyring is broken.
	out, errOut, code := run(t, "", "pr", "view", "7", "-R", "PROJ/repo", "--json", "number", "--jq", ".number")
	if code != 0 || strings.TrimSpace(out) != "7" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

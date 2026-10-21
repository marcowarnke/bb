from __future__ import annotations

import json

import pytest
from conftest import run

from bb import config, credstore, git

pytestmark = pytest.mark.jq


def config_file() -> str:
    return config.path().read_text(encoding="utf-8")


def test_auth_login_keyring(fake, monkeypatch):
    monkeypatch.setenv("BB_TOKEN", "")
    monkeypatch.setenv("BB_HOST", "")
    _, err, code = run("auth", "login", "--hostname", fake.url, "--with-token", stdin="secret\n")
    assert code == 0 and "token stored in keyring" in err, err
    assert "secret" not in config_file()
    assert credstore.get("bb:127.0.0.1", "me") == "secret"
    # Subsequent commands read the token back from the keyring.
    out, err, code = run("pr", "view", "7", "-R", "PROJ/repo", "--json", "number", "--jq", ".number")
    assert code == 0 and out.strip() == "7", err
    out, _, _ = run("auth", "status")
    assert "as me (keyring)" in out and "Git operations protocol: https" in out
    out, _, _ = run("auth", "token")
    assert out == "secret\n"
    _, err, code = run("auth", "logout")
    assert code == 0, err
    with pytest.raises(credstore.NotFound):
        credstore.get("bb:127.0.0.1", "me")


def test_auth_login_insecure_storage(fake, monkeypatch):
    monkeypatch.setenv("BB_TOKEN", "")
    _, err, code = run("auth", "login", "--hostname", fake.url, "--with-token", "--insecure-storage", stdin="secret")
    assert code == 0, err
    assert '"token": "secret"' in config_file()
    with pytest.raises(credstore.NotFound):
        credstore.get("bb:127.0.0.1", "me")


def test_auth_login_keyring_unavailable(fake, monkeypatch):
    credstore.backend = credstore.Mock(OSError("no secret service"))
    monkeypatch.setenv("BB_TOKEN", "")
    _, err, code = run("auth", "login", "--hostname", fake.url, "--with-token", stdin="secret")
    assert code == 0 and "storing it in plain text" in err
    assert '"token": "secret"' in config_file()
    # The file token works even though the keyring is broken.
    out, err, code = run("pr", "view", "7", "-R", "PROJ/repo", "--json", "number", "--jq", ".number")
    assert code == 0 and out.strip() == "7", err


def test_auth_login_bad_token(fake, monkeypatch):
    monkeypatch.setenv("BB_TOKEN", "")
    _, err, code = run("auth", "login", "--hostname", fake.url, "--with-token", stdin="nope")
    assert code == 4 and "validating token" in err and "HTTP 401" in err


def test_config_written_by_go(fake, monkeypatch):
    monkeypatch.setenv("BB_TOKEN", "")
    monkeypatch.setenv("BB_HOST", "")
    config.config_dir().mkdir(parents=True)
    config.path().write_text(
        json.dumps({"hosts": {"127.0.0.1": {"base_url": fake.url, "user": "me", "git_protocol": "ssh"}}}), encoding="utf-8"
    )
    credstore.set("bb:127.0.0.1", "me", "secret")
    out, err, code = run("pr", "view", "7", "-R", "PROJ/repo", "--json", "number", "--jq", ".number")
    assert code == 0 and out.strip() == "7", err
    out, _, _ = run("auth", "status")
    assert "Git operations protocol: ssh" in out


@pytest.mark.parametrize(
    "url,want",
    [
        ("https://bitbucket.corp/scm/proj/repo.git", ("bitbucket.corp", "PROJ", "repo")),
        ("https://user@bitbucket.corp/ctx/scm/~jdoe/Repo.git", ("bitbucket.corp", "~jdoe", "repo")),
        ("ssh://git@bitbucket.corp:7999/proj/repo.git", ("bitbucket.corp", "PROJ", "repo")),
        ("git@bitbucket.corp:proj/repo.git", ("bitbucket.corp", "PROJ", "repo")),
        ("https://bitbucket.corp/projects/PROJ/repos/repo/browse", ("bitbucket.corp", "PROJ", "repo")),
        ("https://bitbucket.corp/users/jdoe/repos/repo/browse", ("bitbucket.corp", "~jdoe", "repo")),
        ("/local/path", None),
    ],
)
def test_parse_remote_url(url, want):
    assert git.parse_remote_url(url) == want

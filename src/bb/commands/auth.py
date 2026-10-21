from __future__ import annotations

import getpass
import os
import sys

from .. import config, output
from ..bitbucket import Client
from ..cli import Command, Error, ExitError, Opts, no_args
from ..output import bold, green, red, success
from .common import prompt


def new_auth_cmd() -> Command:
    cmd = Command(
        "auth <command>",
        short="Authenticate bb with a Bitbucket Data Center host",
        long="""Authenticate with a Bitbucket Data Center host using an HTTP access token
(Profile → Manage account → HTTP access tokens). Tokens need at least
repository write permission to create and comment on pull requests.

Alternatively set BB_HOST and BB_TOKEN; they take precedence over stored credentials.""",
    )
    cmd.add(new_auth_login_cmd(), new_auth_status_cmd(), new_auth_logout_cmd(), new_auth_token_cmd())
    return cmd


def read_password(label: str) -> str:
    """Read a secret without echoing it (when stdin is a terminal)."""
    if output.io.stdin is sys.stdin and sys.stdin.isatty():
        return getpass.getpass(label, stream=output.io.stderr)
    output.io.stderr.write(label)
    line = output.io.stdin.readline()
    output.io.stderr.write("\n")
    return line.rstrip("\n")


def new_auth_login_cmd() -> Command:
    def run(o: Opts) -> None:
        hostname = o.hostname or os.environ.get("BB_HOST", "")
        if not hostname:
            if not output.io.is_tty():
                raise Error("--hostname required when not running interactively")
            hostname = prompt("Bitbucket host (e.g. bitbucket.example.com): ")
        key, base = config.normalize_host(hostname)
        if o.with_token:
            token = output.read_stdin().strip()
        elif output.io.is_tty():
            output.err(f"Create a token at {base}/plugins/servlet/access-tokens/users/manage")
            token = read_password("Paste your HTTP access token: ").strip()
        else:
            raise Error("--with-token required when not running interactively")
        if not token:
            raise Error("no token provided")
        try:
            u = Client(base, token).whoami()
        except Error as e:
            raise Error(f"validating token against {base}: {e}") from e
        slug = u.get("slug", "")
        cfg = config.Config.load()
        old = cfg.hosts.get(key)
        if old is not None and old.user:
            try:
                config.keyring_delete(key, old.user)
            except Exception:
                pass
        h = config.Host(base_url=base, user=slug, git_protocol=o.git_protocol)
        storage = "keyring"
        insecure = o.insecure_storage
        if not insecure:
            try:
                config.keyring_set(key, slug, token)
            except Exception as e:
                output.err(f"warning: could not store token in the OS keyring ({e}); storing it in plain text in {config.path()}")
                insecure = True
        if insecure:
            h.token, storage = token, str(config.path())
        cfg.hosts[key] = h
        cfg.save()
        success(f"Logged in to {key} as {bold(slug)} (token stored in {storage})")

    cmd = Command(
        "login",
        short="Log in to a Bitbucket host",
        long="""Log in to a Bitbucket Data Center host with an HTTP access token.

The token is stored in the OS credential store (macOS Keychain, Windows
Credential Manager, Linux Secret Service). If none is available, or with
--insecure-storage, it is stored in plain text in the config file.""",
        example="""  $ bb auth login --hostname bitbucket.example.com
  $ echo "$TOKEN" | bb auth login --hostname https://example.com/bitbucket --with-token""",
        args=no_args,
        run=run,
    )
    cmd.flag("hostname", "h", help="Bitbucket host or base URL (include the context path if any)")
    cmd.flag("with-token", kind="bool", help="Read token from standard input")
    cmd.flag("insecure-storage", kind="bool", help="Save the token in plain text instead of the OS credential store")
    cmd.flag("git-protocol", "p", default="https", help="Protocol for git operations: {ssh|https}")
    return cmd


def new_auth_status_cmd() -> Command:
    def run(o: Opts) -> None:
        cfg = config.Config.load()
        names = cfg.host_names()
        if o.hostname:
            names = [o.hostname]
        elif h := os.environ.get("BB_HOST", ""):
            try:
                k, _ = config.normalize_host(h)
            except Error:
                k = ""
            if k not in names:
                names.append(h)
        if not names:
            raise ExitError(1, config.err_no_host())
        failed = False
        for n in names:
            try:
                key, h = cfg.resolve(n)
            except Error as e:
                key = e.key if isinstance(e, config.ResolveError) else ""
                output.out(bold(key))
                output.out(f"  {red('X')} {e}")
                failed = True
                continue
            output.out(bold(key))
            try:
                u = Client(h.base_url, h.token).whoami()
            except Error as e:
                output.out(f"  {red('X')} Failed to log in to {key} using token ({h.token_source}): {e}")
                failed = True
                continue
            output.out(f"  {green('✓')} Logged in to {key} as {bold(u.get('slug', ''))} ({h.token_source})")
            output.out(f"  - API base URL: {h.base_url}")
            if h.git_protocol:
                output.out(f"  - Git operations protocol: {h.git_protocol}")
            tok = h.token
            if not o.show_token and len(tok) > 4:
                tok = tok[:4] + "*" * 12
            output.out(f"  - Token: {tok}")
        if failed:
            raise ExitError(1)

    cmd = Command("status", short="Display authentication state", args=no_args, run=run)
    cmd.flag("hostname", "h", help="Check only a specific host")
    cmd.flag("show-token", "t", kind="bool", help="Display the auth token")
    return cmd


def new_auth_logout_cmd() -> Command:
    def run(o: Opts) -> None:
        cfg = config.Config.load()
        hostname = o.hostname
        if not hostname:
            if len(cfg.hosts) != 1:
                raise Error(f"--hostname required (logged in to {len(cfg.hosts)} hosts)")
            hostname = cfg.host_names()[0]
        key, _ = config.normalize_host(hostname)
        h = cfg.hosts.get(key)
        if h is None:
            raise Error(f"not logged in to {key}")
        try:
            config.keyring_delete(key, h.user)
        except Exception as e:
            raise Error(f"removing token from the OS keyring: {e}") from e
        del cfg.hosts[key]
        cfg.save()
        success(f"Logged out of {key} account {h.user}")

    cmd = Command("logout", short="Log out of a Bitbucket host", args=no_args, run=run)
    cmd.flag("hostname", "h", help="The host to log out of")
    return cmd


def new_auth_token_cmd() -> Command:
    def run(o: Opts) -> None:
        _, h = config.Config.load().resolve(o.hostname)
        output.out(h.token)

    cmd = Command("token", short="Print the auth token bb is configured to use", args=no_args, run=run)
    cmd.flag("hostname", "h", help="The host to print the token for")
    return cmd

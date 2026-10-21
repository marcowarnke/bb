from __future__ import annotations

import json
import os
import posixpath
import re
from urllib.parse import quote_plus

from .. import config, git, output
from ..bitbucket import path_escape
from ..cli import Command, Error, Opts, maximum_args
from .common import arg_or
from .resolve import Repo, resolve_repo

SHA_RE = re.compile(r"^[0-9a-f]{7,40}$")


def new_browse_cmd() -> Command:
    def run(o: Opts) -> None:
        if o.changed("branch") and o.changed("commit"):
            raise Error("specify only one of --branch or --commit")
        r = resolve_repo(o.repo)
        base = repo_web_url(r)
        commit = o.commit
        if commit == "last":
            commit = git.run("rev-parse", "HEAD")
        u = browse_url(base, r, arg_or(o), o.branch, commit, o.settings, o.pull_requests)
        if o.no_browser:
            output.out(u)
            return
        output.open_browser(u)

    cmd = Command(
        "browse [<number> | <path>[:<line>[-<line>]] | <commit>]",
        short="Open the repository in the browser",
        long="""Open the Bitbucket repository in the web browser.

The argument can be a pull request number, a commit SHA, or a file or
directory path (relative to the current directory) with an optional line or
line range, e.g. "src/main.go:42" or "src/main.go:10-20".

Files are shown on --branch or --commit if given; otherwise on the current
branch if it has been pushed (has an upstream), else on the default branch.""",
        example="""  $ bb browse
  $ bb browse 42
  $ bb browse src/main.go:42
  $ bb browse src/ --branch release/1.0
  $ bb browse --commit          # last commit
  $ bb browse -n src/main.go    # print the URL instead""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("repo", "R", help="Select another repository using the `[HOST/]PROJECT/REPO` format")
    cmd.flag("branch", "b", help="Select another branch by passing in the branch name")
    cmd.flag("commit", "c", no_opt="last", help="Select another commit by passing in the commit SHA, default is the last commit")
    cmd.flag("no-browser", "n", kind="bool", help="Print destination URL instead of opening the browser")
    cmd.flag("settings", "s", kind="bool", help="Open repository settings")
    cmd.flag("pull-requests", "p", kind="bool", help="Open the repository's pull requests")
    return cmd


def repo_web_url(r: Repo) -> str:
    """The repository's web URL from the configured host; no API call or token needed."""
    cfg = config.Config.load()
    h = None
    err: Error | None = None
    try:
        _, h = cfg.resolve(r.host)
    except config.ResolveError as e:
        h = e.host  # a missing token doesn't matter here
    except Error as e:
        err = e
    if h is not None and h.base_url:
        base = h.base_url
    elif r.host:
        base = "https://" + r.host
    else:
        raise err or Error("no host")
    if r.project.startswith("~"):
        return f"{base}/users/{path_escape(r.project[1:].lower())}/repos/{path_escape(r.slug)}"
    return f"{base}/projects/{path_escape(r.project)}/repos/{path_escape(r.slug)}"


def browse_url(base: str, r: Repo, arg: str, branch: str, commit: str, settings: bool, prs: bool) -> str:
    if settings:
        return base + "/settings"
    if prs:
        return base + "/pull-requests"
    at = commit or ("refs/heads/" + branch if branch else "")
    if not arg:
        if commit:
            return base + "/commits/" + path_escape(commit)
        return with_at(base + "/browse", at, "")
    num = arg.removeprefix("#")
    if num.isascii() and num.isdigit() and int(num) > 0:
        return f"{base}/pull-requests/{int(num)}"
    if SHA_RE.match(arg) and not os.path.exists(arg):
        return base + "/commits/" + arg
    p, lines = split_path_line(arg)
    if r.remote:
        # Paths are relative to the current directory inside the checkout.
        try:
            p = posixpath.join(git.run("rev-parse", "--show-prefix"), p)
        except Error:
            pass
        if not at:
            at = pushed_branch_ref()
    if os.name == "nt":
        p = p.replace("\\", "/")
    p = posixpath.normpath("/" + p).strip("/")
    segs = [path_escape(s) for s in p.split("/") if s]
    return with_at(base + "/browse/" + "/".join(segs), at, lines)


def with_at(u: str, at: str, fragment: str) -> str:
    if at:
        u += "?at=" + quote_plus(at, safe="")
    if fragment:
        u += "#" + fragment
    return u


def split_path_line(arg: str) -> tuple[str, str]:
    """Split "file:42" or "file:10-20" into the path and the Bitbucket line fragment ("42", "10-20")."""
    i = arg.rfind(":")
    if i < 0:
        return arg, ""
    p, spec = arg[:i], arg[i + 1 :]
    frm, dash, to = spec.partition("-")
    a = _atoi(frm)
    if a is None or a < 1:
        raise Error(f"invalid line number {json.dumps(spec)} in {json.dumps(arg)}")
    if not dash:
        return p, frm
    b = _atoi(to)
    if b is None or b < a:
        raise Error(f"invalid line range {json.dumps(spec)} in {json.dumps(arg)}")
    return p, frm + "-" + to


def _atoi(s: str) -> int | None:
    t = s[1:] if s[:1] in "+-" else s
    return int(s) if t.isascii() and t.isdigit() else None


def pushed_branch_ref() -> str:
    """The current branch's upstream ref on the remote, or "" (default branch) when it hasn't been pushed."""
    try:
        up = git.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
    except Error:
        return ""
    if "/" in up:
        return "refs/heads/" + up.split("/", 1)[1]
    return ""

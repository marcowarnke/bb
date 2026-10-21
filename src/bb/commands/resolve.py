from __future__ import annotations

import json
import os
from dataclasses import dataclass
from urllib.parse import urlsplit

from .. import bitbucket, config, git
from ..bitbucket import Client
from ..cli import Error, ExitError


@dataclass
class Repo:
    """A repository on a host. remote is the matching git remote in the current checkout, if any."""

    host: str = ""
    project: str = ""
    slug: str = ""
    remote: str = ""

    def full_name(self) -> str:
        return self.project + "/" + self.slug

    def path(self) -> str:
        return bitbucket.repo_path(self.project, self.slug)

    def pr_path(self, pr_id: int) -> str:
        return bitbucket.pr_path(self.project, self.slug, pr_id)


def parse_repo_arg(s: str) -> Repo:
    """PROJECT/REPO, HOST/PROJECT/REPO or a URL."""
    if "://" in s or "@" in s:
        parsed = git.parse_remote_url(s)
        if parsed is None:
            raise Error(f"could not parse repository URL {json.dumps(s)}")
        return Repo(*parsed)
    parts = s.strip("/").split("/")
    if len(parts) == 2:
        return Repo(project=git.normalize_project(parts[0]), slug=parts[1].lower())
    if len(parts) == 3:
        return Repo(host=parts[0], project=git.normalize_project(parts[1]), slug=parts[2].lower())
    raise Error(f'expected the "[HOST/]PROJECT/REPO" format, got {json.dumps(s)}')


def resolve_repo(flag: str) -> Repo:
    """The target repo from -R, BB_REPO or git remotes."""
    if not flag:
        flag = os.environ.get("BB_REPO", "")
    cfg = config.Config.load()
    try:
        remotes = git.remotes()
    except Error:
        remotes = []
    if flag:
        r = parse_repo_arg(flag)
        for rm in remotes:
            p = git.parse_remote_url(rm.url)
            if p is not None and p[1] == r.project and p[2] == r.slug:
                r.remote = rm.name
                break
        return r
    if not remotes:
        raise Error("not in a git repository with a Bitbucket remote; use -R PROJECT/REPO")
    fallback: Repo | None = None
    for rm in remotes:
        p = git.parse_remote_url(rm.url)
        if p is None:
            continue
        r = Repo(p[0], p[1], p[2], rm.name)
        if r.host in cfg.hosts:
            return r
        if fallback is None:
            fallback = r
    if fallback is None:
        raise Error("none of the git remotes point to a Bitbucket repository; use -R PROJECT/REPO")
    # SSH hostnames often differ from the web host; let the config decide.
    if len(cfg.hosts) == 1 or os.environ.get("BB_HOST"):
        fallback.host = ""
    return fallback


def client_for(host: str) -> Client:
    cfg = config.Config.load()
    try:
        _, h = cfg.resolve(host)
    except Error as e:
        raise ExitError(4, e) from e
    c = Client(h.base_url, h.token)
    if not os.environ.get("BB_TOKEN"):
        c.user_slug = h.user
    return c


def repo_client(repo_flag: str) -> tuple[Repo, Client]:
    """Resolve both the repo and an API client for it."""
    r = resolve_repo(repo_flag)
    return r, client_for(r.host)


def resolve_pr(c: Client, r: Repo, arg: str) -> dict:
    """A PR from a number, URL, branch name, or the current branch."""
    if arg:
        n = _atoi(arg.removeprefix("#"))
        if n is not None:
            return c.get_pr(r.project, r.slug, n)
        if "://" in arg:
            try:
                parsed = parse_pr_url(urlsplit(arg).path)
            except ValueError:
                parsed = None
            if parsed is not None:
                r.project, r.slug = parsed[0], parsed[1]
                return c.get_pr(parsed[0], parsed[1], parsed[2])
            raise Error(f"invalid pull request URL {json.dumps(arg)}")
    else:
        arg = git.current_branch()
    for state in ("OPEN", "ALL"):
        q = {"state": state, "direction": "OUTGOING", "at": "refs/heads/" + arg, "order": "NEWEST"}
        prs = c.paginate(r.path() + "/pull-requests", q, 1, lambda p: bitbucket.ref(p, "fromRef").get("displayId") == arg)
        if prs:
            return prs[0]
    raise Error(f"no pull requests found for branch {json.dumps(arg)}")


def _atoi(s: str) -> int | None:
    """strconv.Atoi: optional sign, ASCII digits only."""
    t = s[1:] if s[:1] in "+-" else s
    if not t or not (t.isascii() and t.isdigit()):
        return None
    return int(s)


def parse_pr_url(path: str) -> tuple[str, str, int] | None:
    """/projects/P/repos/r/pull-requests/N[/...] and /users/u/repos/..."""
    parts = path.strip("/").split("/")
    for i in range(len(parts) - 4):
        if parts[i] in ("projects", "users") and parts[i + 2] == "repos" and parts[i + 4] == "pull-requests" and i + 5 < len(parts):
            n = _atoi(parts[i + 5])
            if n is None:
                return None
            project = parts[i + 1]
            if parts[i] == "users":
                project = "~" + project
            return git.normalize_project(project), parts[i + 3], n
    return None


def me_or(c: Client, v: str) -> str:
    """Replace "@me" with the current user's slug."""
    if v == "@me":
        return c.current_user()
    return v

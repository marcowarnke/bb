"""Wraps the git CLI and parses Bitbucket remote URLs."""

from __future__ import annotations

import subprocess
import sys
from dataclasses import dataclass
from urllib.parse import urlsplit

from .cli import Error
from .config import hostname


def run(*args: str) -> str:
    try:
        p = subprocess.run(["git", *args], capture_output=True, stdin=subprocess.DEVNULL)
    except FileNotFoundError:
        raise Error(f"git {' '.join(args)}: git not found on PATH") from None
    if p.returncode != 0:
        msg = p.stderr.decode("utf-8", "replace").strip() or f"exit status {p.returncode}"
        raise Error(f"git {' '.join(args)}: {msg}")
    return p.stdout.decode("utf-8", "replace").rstrip("\n").replace("\r\n", "\n")


def passthrough(*args: str) -> None:
    """Run git with stdin attached and its output on stderr."""
    try:
        p = subprocess.run(["git", *args], stdout=sys.__stderr__, stderr=sys.__stderr__)
    except FileNotFoundError:
        raise Error("git not found on PATH") from None
    if p.returncode != 0:
        raise Error(f"exit status {p.returncode}")


def current_branch() -> str:
    try:
        return run("symbolic-ref", "--quiet", "--short", "HEAD")
    except Error:
        raise Error("could not determine current branch (detached HEAD or not a git repository)") from None


def has_local_branch(name: str) -> bool:
    try:
        run("rev-parse", "--verify", "--quiet", "refs/heads/" + name)
        return True
    except Error:
        return False


@dataclass
class Remote:
    name: str
    url: str


def remotes() -> list[Remote]:
    """Fetch remotes, upstream first, then origin, like gh."""
    out = run("remote", "-v")
    rs: list[Remote] = []
    seen = set()
    for line in out.split("\n"):
        f = line.split()
        if len(f) < 3 or f[2] != "(fetch)" or f[0] in seen:
            continue
        seen.add(f[0])
        rs.append(Remote(f[0], f[1]))
    rank = {"upstream": 0, "origin": 1}
    rs.sort(key=lambda r: rank.get(r.name, 2))
    return rs


def parse_remote_url(raw: str) -> tuple[str, str, str] | None:
    """Host, project key and repo slug from a Bitbucket Data Center clone or browse URL, e.g.

    https://bitbucket.corp/scm/proj/repo.git
    https://bitbucket.corp/ctx/scm/~user/repo.git
    ssh://git@bitbucket.corp:7999/proj/repo.git
    git@bitbucket.corp:proj/repo.git
    https://bitbucket.corp/projects/PROJ/repos/repo/browse
    """
    if "://" not in raw:
        # scp-like syntax: user@host:path
        at, colon = raw.find("@"), raw.find(":")
        if colon < 0 or colon < at:
            return None
        raw = "ssh://" + raw[:colon] + "/" + raw[colon + 1 :]
    try:
        u = urlsplit(raw)
    except ValueError:
        return None
    host = hostname(u.netloc)
    if not host:
        return None
    parts = [p for p in u.path.split("/") if p]
    project = slug = ""
    for i, p in enumerate(parts):
        if p == "scm" and i + 2 < len(parts):
            project, slug = parts[i + 1], parts[i + 2]
            break
        if p in ("projects", "users") and i + 3 < len(parts) and parts[i + 2] == "repos":
            project, slug = parts[i + 1], parts[i + 3]
            if p == "users":
                project = "~" + project
            break
    if not project and len(parts) >= 2:
        project, slug = parts[-2], parts[-1]
    slug = slug.removesuffix(".git")
    if not project or not slug:
        return None
    return host, normalize_project(project), slug.lower()


def normalize_project(p: str) -> str:
    """Upper-case project keys; personal projects (~user) keep case."""
    if p.startswith("~"):
        return p
    return p.upper()

"""Dynamic shell completion. Shells show the text after "\\t" as a description
(zsh, fish, powershell, bash v2), so PR numbers come with their titles. Every
completer fails silently: a slow or unreachable server must never break the shell."""

from __future__ import annotations

from collections.abc import Callable

from ..bitbucket import Client, path_escape, ref, user_of
from ..cli import COMP_DEFAULT, COMP_KEEP_ORDER, COMP_NOFILE, COMP_NOSPACE, Command, Opts
from ..output import truncate
from .resolve import Repo, client_for, repo_client

COMPLETION_TIMEOUT = 5.0

USER_FLAGS = {"reviewer", "author", "add-reviewer", "remove-reviewer"}
BRANCH_FLAGS = {"base", "head", "branch"}

Completer = Callable[[Opts, str], tuple[list[str], int]]


def register_completions(c: Command) -> None:
    """Wire completers into the command tree by argument shape and flag name,
    so new commands pick them up automatically."""
    if "<number>" in c.use and c.complete_args is None:
        state = "DECLINED" if c.name == "reopen" else "OPEN"
        c.complete_args = complete_prs(state, c.name in ("browse", "diff"))
    for f in c.flags + c.persistent:
        if f.name in USER_FLAGS:
            f.complete = complete_users
        elif f.name in BRANCH_FLAGS:
            f.complete = complete_branches
        elif f.name == "repo":
            f.complete = complete_repos
        elif f.name == "state" and c.name == "list":
            f.complete = ["open", "closed", "merged", "all"]
    for sub in c.commands:
        register_completions(sub)


def completion_client(o: Opts) -> tuple[Repo, Client]:
    r, c = repo_client(getattr(o, "repo", ""))
    c.timeout = COMPLETION_TIMEOUT
    return r, c


def complete_prs(state: str, files: bool) -> Completer:
    """PR numbers, newest first, described as "title · author · branch". files:
    also fall back to file completion (browse paths, diff paths after the PR)."""

    def complete(o: Opts, to_complete: str) -> tuple[list[str], int]:
        file_dir = COMP_DEFAULT if files else COMP_NOFILE
        if o.args or (to_complete and to_complete.lstrip("#0123456789") != ""):
            return [], file_dir
        try:
            r, c = completion_client(o)
            prs = c.paginate(r.path() + "/pull-requests", {"state": state, "order": "NEWEST"}, 100)
        except Exception:
            return [], file_dir
        prefix = to_complete.removeprefix("#")
        out = []
        for p in prs:
            n = str(p.get("id", 0))
            if not n.startswith(prefix):
                continue
            title = p.get("title", "")
            if p.get("draft"):
                title = "[draft] " + title
            out.append(f"{n}\t{one_line(title)} · {user_of(p.get('author')).get('slug', '')} · {ref(p, 'fromRef').get('displayId', '')}")
        if files and not out:
            return out, COMP_DEFAULT
        return out, COMP_NOFILE | COMP_KEEP_ORDER

    return complete


def one_line(s: str) -> str:
    return " ".join(truncate(s, 80).split())


def last_item(to_complete: str) -> tuple[str, str]:
    """Split comma-separated flag values: only the last item is completed."""
    i = to_complete.rfind(",")
    if i >= 0:
        return to_complete[: i + 1], to_complete[i + 1 :]
    return "", to_complete


def complete_users(o: Opts, to_complete: str) -> tuple[list[str], int]:
    head, item = last_item(to_complete)
    out = []
    if "@me".startswith(item) and o.cmd.name == "list":
        out.append(head + "@me\tyourself")
    try:
        _, c = completion_client(o)
        users = c.paginate("rest/api/1.0/users", {"filter": item}, 25)
    except Exception:
        return out, COMP_NOFILE
    out += [f"{head}{u.get('slug', '')}\t{u.get('displayName', '')}" for u in users]
    return out, COMP_NOFILE


def complete_branches(o: Opts, to_complete: str) -> tuple[list[str], int]:
    try:
        r, c = completion_client(o)
        refs = c.paginate(r.path() + "/branches", {"filterText": to_complete, "orderBy": "MODIFICATION"}, 50)
    except Exception:
        return [], COMP_NOFILE
    return [x.get("displayId", "") for x in refs], COMP_NOFILE | COMP_KEEP_ORDER


def complete_repos(o: Opts, to_complete: str) -> tuple[list[str], int]:
    """ "PROJECT/" first, then that project's repositories. Candidates keep the
    typed casing because shells filter them by prefix."""
    try:
        c = client_for("")
    except Exception:
        return [], COMP_NOFILE
    c.timeout = COMPLETION_TIMEOUT
    project, slash, name = to_complete.partition("/")
    out = []
    try:
        if not slash:
            for p in c.paginate("rest/api/1.0/projects"):
                key = p.get("key", "")
                if key.upper().startswith(to_complete.upper()):
                    out.append(to_complete + key[len(to_complete) :] + "/\t" + one_line(p.get("name", "")))
            return out, COMP_NOFILE | COMP_NOSPACE
        for repo in c.paginate(f"rest/api/1.0/projects/{path_escape(project.upper())}/repos"):
            if repo.get("slug", "").startswith(name.lower()):
                out.append(f"{project}/{repo.get('slug', '')}\t{one_line(repo.get('name', ''))}")
    except Exception:
        return [], COMP_NOFILE
    return out, COMP_NOFILE

from __future__ import annotations

import json

from .. import bitbucket, git, output
from ..bitbucket import Client, path_escape, ref, user_of, web_url
from ..cli import Command, Error, Opts, maximum_args, no_args
from ..output import cyan, success
from .common import arg_or, prompt, read_body, split_list
from .pr import gh_state, merge_blockers
from .resolve import Repo, me_or, repo_client, resolve_pr


def ref_json(branch: str, r: Repo) -> dict:
    return {"id": "refs/heads/" + branch, "repository": {"slug": r.slug, "project": {"key": r.project}}}


def reviewers_json(names: list[str]) -> list[dict]:
    return [{"user": {"name": n}} for n in names]


def resolve_users(c: Client, qs: list[str]) -> list[str]:
    names = []
    for q in qs:
        q = me_or(c, q)
        try:
            u = c.resolve_user(q)
        except Error as e:
            raise Error(f"reviewer {json.dumps(q)}: {e}") from e
        names.append(u.get("name", ""))
    return names


def default_reviewers(c: Client, r: Repo, head: str, base: str) -> list[str]:
    repo = c.get_repo(r.project, r.slug)
    rid = str(repo.get("id", 0))
    q = {"sourceRepoId": rid, "targetRepoId": rid, "sourceRefId": "refs/heads/" + head, "targetRefId": "refs/heads/" + base}
    path = f"rest/default-reviewers/1.0/projects/{path_escape(r.project)}/repos/{path_escape(r.slug)}/reviewers"
    return [u.get("name", "") for u in c.do("GET", path, q) or []]


def dedupe(xs: list[str], exclude: str) -> list[str]:
    seen = {exclude.lower()}
    out = []
    for x in xs:
        if x.lower() not in seen:
            seen.add(x.lower())
            out.append(x)
    return out


def new_pr_create_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        head, base, title = o.head, o.base, o.title
        if not head:
            try:
                head = git.current_branch()
            except Error as e:
                raise Error(f"{e}; use --head") from e
        if not base:
            try:
                base = c.default_branch(r.project, r.slug)
            except Error as e:
                raise Error(f"determining default branch: {e}") from e
        if head == base:
            raise Error(f"head branch {json.dumps(head)} is the same as base branch")
        body, body_given = read_body(o, o.body, o.body_file)
        if o.fill or o.fill_first:
            t, b = commit_summary(r, base, head, o.fill_first)
            if not title:
                title = t
            if not body_given:
                body = b
        if not title:
            if not output.io.is_tty():
                raise Error("must provide `--title` and `--body` (or `--fill`) when not running interactively")
            title = prompt("Title: ")
            if not body_given and not o.fill:
                body = prompt("Body: ")
        if not title:
            raise Error("title cannot be blank")
        if not c.branch_exists(r.project, r.slug, head):
            raise Error(f"branch {json.dumps(head)} not found in {r.full_name()}; push it first: git push -u {r.remote or 'origin'} {head}")
        existing = c.paginate(
            r.path() + "/pull-requests",
            {"state": "OPEN", "direction": "OUTGOING", "at": "refs/heads/" + head},
            1,
            lambda p: ref(p, "fromRef").get("displayId") == head and ref(p, "toRef").get("displayId") == base,
        )
        if existing:
            raise Error(
                f"a pull request for branch {json.dumps(head)} into branch {json.dumps(base)} already exists:\n{web_url(existing[0])}"
            )
        names = resolve_users(c, split_list(o.reviewer))
        if not o.no_default_reviewers:
            try:
                names += default_reviewers(c, r, head, base)
            except bitbucket.APIError as e:
                if e.status != 404:
                    output.err(f"warning: could not fetch default reviewers: {e}")
            except Error as e:
                output.err(f"warning: could not fetch default reviewers: {e}")
        try:
            me = c.current_user()
        except Error:
            me = ""
        names = dedupe(names, me)
        payload = {
            "title": title,
            "description": body,
            "fromRef": ref_json(head, r),
            "toRef": ref_json(base, r),
            "reviewers": reviewers_json(names),
        }
        if o.draft:
            payload["draft"] = True
        if o.dry_run:
            output.out(output.to_json(payload, indent=True))
            return
        if output.io.is_tty():
            output.err(f"\nCreating pull request for {cyan(head)} into {cyan(base)} in {r.full_name()}\n")
        pr = c.do("POST", r.path() + "/pull-requests", None, payload) or {}
        if o.web:
            output.open_browser(web_url(pr))
            return
        output.out(web_url(pr))

    cmd = Command(
        "create",
        short="Create a pull request",
        aliases=["new"],
        long="""Create a pull request on Bitbucket.

The head branch must already be pushed. Repository default reviewers are added
automatically (as the Bitbucket UI does) unless --no-default-reviewers is set.

Reviewers can be given as username, slug, email, or a unique name fragment.""",
        example="""  $ bb pr create --title "Fix bug" --body "Details" --reviewer alice,bob
  $ bb pr create --fill --base develop --draft
  $ bb pr create -t "Add feature" -F body.md""",
        args=no_args,
        run=run,
    )
    cmd.flag("title", "t", help="Title for the pull request")
    cmd.flag("body", "b", help="Body for the pull request")
    cmd.flag("body-file", "F", help='Read body text from `file` (use "-" to read from standard input)')
    cmd.flag("base", "B", help="The `branch` into which you want your code merged (default: repo default branch)")
    cmd.flag("head", "H", help="The `branch` that contains commits for your pull request (default: current branch)")
    cmd.flag("reviewer", "r", kind="strs", help="Request reviews from people by their `login`")
    cmd.flag("draft", "d", kind="bool", help="Mark pull request as a draft (Bitbucket 8.18+)")
    cmd.flag("fill", "f", kind="bool", help="Use commit info for title and body")
    cmd.flag("fill-first", kind="bool", help="Use first commit info for title and body")
    cmd.flag("dry-run", kind="bool", help="Print the request payload instead of creating the pull request")
    cmd.flag("no-default-reviewers", kind="bool", help="Do not add the repository's default reviewers")
    cmd.flag("web", "w", kind="bool", help="Open the created pull request in the browser")
    return cmd


def commit_summary(r: Repo, base: str, head: str, first_only: bool) -> tuple[str, str]:
    """gh's --fill: one commit → its subject/body; many → branch name as title
    and a bullet list of subjects as body."""
    base_ref = f"{r.remote}/{base}" if r.remote else base
    out = git.run("log", "--reverse", "--format=%s%x1f%b%x1e", f"{base_ref}..{head}")
    commits = []
    for rec in out.split("\x1e"):
        rec = rec.lstrip("\n")
        if not rec:
            continue
        subject, _, body = rec.partition("\x1f")
        commits.append((subject, body.strip()))
    if not commits:
        raise Error(f"no commits between {base_ref} and {head}")
    if len(commits) == 1 or first_only:
        return commits[0]
    body = "\n".join(f"- {s}" for s, _ in commits)
    t = head[head.rfind("/") + 1 :].replace("-", " ").replace("_", " ")
    if t:
        t = t[0].upper() + t[1:]
    return t, body


def update_pr(c: Client, r: Repo, pr: dict, mutate) -> dict:  # type: ignore[no-untyped-def]
    """PUT the PR back with modifications; Bitbucket requires the full reviewer list and the current version."""
    payload = {
        "version": pr.get("version", 0),
        "title": pr.get("title", ""),
        "description": pr.get("description", ""),
        "reviewers": reviewers_json([user_of(rv).get("name", "") for rv in pr.get("reviewers") or []]),
        "toRef": {"id": ref(pr, "toRef").get("id", "")},
    }
    mutate(payload)
    return c.do("PUT", r.pr_path(pr.get("id", 0)), None, payload) or {}


def new_pr_edit_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        body, body_given = read_body(o, o.body, o.body_file)
        add = resolve_users(c, split_list(o.add_reviewer))
        remove = resolve_users(c, split_list(o.remove_reviewer))
        if not o.title and not body_given and not o.base and not add and not remove:
            raise Error("specify at least one of --title, --body, --body-file, --base, --add-reviewer, --remove-reviewer")

        def mutate(p: dict) -> None:
            if o.title:
                p["title"] = o.title
            if body_given:
                p["description"] = body
            if o.base:
                p["toRef"] = {"id": "refs/heads/" + o.base}
            names = [user_of(rv).get("name", "") for rv in pr.get("reviewers") or [] if user_of(rv).get("name", "") not in remove]
            p["reviewers"] = reviewers_json(dedupe(names + add, user_of(pr.get("author")).get("name", "")))

        output.out(web_url(update_pr(c, r, pr, mutate)))

    cmd = Command(
        "edit [<number> | <url> | <branch>]",
        short="Edit a pull request",
        example="""  $ bb pr edit 23 --title "I found a bug" --body "Nothing works"
  $ bb pr edit 23 --add-reviewer alice,bob --remove-reviewer carol""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("title", "t", help="Set the new title")
    cmd.flag("body", "b", help="Set the new body")
    cmd.flag("body-file", "F", help='Read body text from `file` (use "-" to read from standard input)')
    cmd.flag("base", "B", help="Change the base `branch` for this pull request")
    cmd.flag("add-reviewer", kind="strs", help="Add reviewers by their `login`")
    cmd.flag("remove-reviewer", kind="strs", help="Remove reviewers by their `login`")
    return cmd


def new_pr_ready_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        update_pr(c, r, pr, lambda p: p.__setitem__("draft", o.undo))
        if o.undo:
            success(f'Pull request {r.full_name()}#{pr.get("id", 0)} is converted to "draft"')
        else:
            success(f'Pull request {r.full_name()}#{pr.get("id", 0)} is marked as "ready for review"')

    cmd = Command(
        "ready [<number> | <url> | <branch>]",
        short="Mark a pull request as ready for review (Bitbucket 8.18+)",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("undo", kind="bool", help='Convert a pull request to "draft"')
    return cmd


def new_pr_merge_cmd() -> Command:
    def run(o: Opts) -> None:
        chosen = [s for s, on in (("no-ff", o.merge), ("squash", o.squash), ("rebase-no-ff", o.rebase)) if on]
        if len(chosen) > 1:
            raise Error("only one of --merge, --squash, --rebase can be enabled")
        strategy = chosen[0] if chosen else ""
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        pid = pr.get("id", 0)
        if pr.get("state") != "OPEN":
            raise Error(f"pull request {r.full_name()}#{pid} is not open ({gh_state(pr)})")
        ms = c.merge_status(r.project, r.slug, pid)
        if not ms.get("canMerge"):
            raise Error(f"pull request {r.full_name()}#{pid} is not mergeable:\n  - " + "\n  - ".join(merge_blockers(ms)))
        if o.dry_run:
            how = strategy or "the repository's default strategy"
            success(f"Pull request {r.full_name()}#{pid} can be merged ({how}); nothing done (--dry-run)")
            return
        payload: dict = {}
        if strategy:
            payload["strategyId"] = strategy
        msg = (o.subject.strip() + "\n\n" + o.body).strip()
        if msg:
            payload["message"] = msg
        c.do("POST", r.pr_path(pid) + "/merge", {"version": str(pr.get("version", 0))}, payload)
        success(f"Merged pull request {r.full_name()}#{pid} ({pr.get('title', '')})")
        if o.delete_branch:
            delete_pr_branch(c, r, pr)

    cmd = Command(
        "merge [<number> | <url> | <branch>]",
        short="Merge a pull request",
        long="""Merge a pull request on Bitbucket.

Without a strategy flag the repository's default merge strategy is used.
--merge → no-ff, --squash → squash, --rebase → rebase-no-ff
(the strategy must be enabled for the repository).

If the pull request can't be merged, the reasons are listed: merge conflicts
and failing merge checks (required approvals, builds, open tasks, …).
--dry-run only runs these checks: exit 0 if mergeable, 1 with the reasons if not.""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("merge", "m", kind="bool", help="Merge the commits with the base branch (no-ff)")
    cmd.flag("squash", "s", kind="bool", help="Squash the commits into one commit and merge it into the base branch")
    cmd.flag("rebase", "r", kind="bool", help="Rebase the commits onto the base branch")
    cmd.flag("delete-branch", "d", kind="bool", help="Delete the local and remote branch after merge")
    cmd.flag("dry-run", kind="bool", help="Check whether the pull request can be merged without merging it")
    cmd.flag("subject", "t", help="Subject `text` for the merge commit")
    cmd.flag("body", "b", help="Body `text` for the merge commit")
    return cmd


def delete_pr_branch(c: Client, r: Repo, pr: dict) -> None:
    """Remove the head branch remotely (same-repo PRs only) and locally."""
    head = ref(pr, "fromRef").get("displayId", "")
    if not bitbucket.is_cross_repository(pr):
        path = f"rest/branch-utils/1.0/projects/{path_escape(r.project)}/repos/{path_escape(r.slug)}/branches"
        try:
            c.do("DELETE", path, None, {"name": ref(pr, "fromRef").get("id", ""), "dryRun": False})
        except Error as e:
            raise Error(f"deleting remote branch {head}: {e}") from e
        success(f"Deleted remote branch {head}")
    if not r.remote or not git.has_local_branch(head):
        return
    try:
        current = git.current_branch()
    except Error:
        current = ""
    if current == head:
        git.run("checkout", ref(pr, "toRef").get("displayId", ""))
    git.run("branch", "-D", head)
    success(f"Deleted local branch {head}")


def pr_transition(name: str, short: str, action: str, verb: str) -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        pid = pr.get("id", 0)
        if o.comment:
            c.do("POST", r.pr_path(pid) + "/comments", None, {"text": o.comment})
        c.do("POST", r.pr_path(pid) + "/" + action, {"version": str(pr.get("version", 0))}, {})
        success(f"{verb} pull request {r.full_name()}#{pid} ({pr.get('title', '')})")
        if getattr(o, "delete_branch", False):
            delete_pr_branch(c, r, pr)

    cmd = Command(name + " [<number> | <url> | <branch>]", short=short, args=maximum_args(1), run=run)
    cmd.flag("comment", "c", help="Leave a closing comment")
    if action == "decline":
        cmd.flag("delete-branch", "d", kind="bool", help="Delete the local and remote branch after close")
    return cmd


def new_pr_close_cmd() -> Command:
    return pr_transition("close", "Close (decline) a pull request", "decline", "Closed")


def new_pr_reopen_cmd() -> Command:
    return pr_transition("reopen", "Reopen a closed (declined) pull request", "reopen", "Reopened")

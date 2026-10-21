from __future__ import annotations

from .. import bitbucket, output
from ..bitbucket import Client, ref, user_of, web_url
from ..cli import Command, Error, ExitError, Opts, maximum_args, no_args
from ..output import Exporter, TabWriter, add_json_flags, bold, cyan, gray, green, magenta, pick, red, truncate, yellow
from .common import ago, arg_or, ts
from .resolve import Repo, me_or, repo_client, resolve_pr


def new_pr_cmd() -> Command:
    from . import pr_misc, pr_review, pr_write

    cmd = Command(
        "pr <command>",
        short="Manage pull requests",
        aliases=["pull-request"],
        long="""Work with Bitbucket pull requests.

A pull request can be supplied as argument in any of the following formats:
- by number, e.g. "123";
- by URL, e.g. "https://bitbucket.example.com/projects/PROJ/repos/repo/pull-requests/123";
- by the name of its head branch, e.g. "feature/foo".
Without an argument, the pull request for the current branch is used.""",
    )
    cmd.pflag("repo", "R", help="Select another repository using the `[HOST/]PROJECT/REPO` format")
    cmd.add(
        new_pr_list_cmd(),
        new_pr_view_cmd(),
        new_pr_status_cmd(),
        pr_misc.new_pr_diff_cmd(),
        pr_misc.new_pr_checks_cmd(),
        pr_misc.new_pr_checkout_cmd(),
        pr_write.new_pr_create_cmd(),
        pr_write.new_pr_edit_cmd(),
        pr_write.new_pr_ready_cmd(),
        pr_review.new_pr_comment_cmd(),
        pr_review.new_pr_review_cmd(),
        pr_review.new_pr_threads_cmd(),
        pr_write.new_pr_merge_cmd(),
        pr_write.new_pr_close_cmd(),
        pr_write.new_pr_reopen_cmd(),
    )
    return cmd


PR_FIELDS = [
    "author",
    "baseRefName",
    "baseRefOid",
    "body",
    "closed",
    "closedAt",
    "commentCount",
    "comments",
    "commits",
    "createdAt",
    "files",
    "headRefName",
    "headRefOid",
    "headRepository",
    "id",
    "isCrossRepository",
    "isDraft",
    "latestReviews",
    "mergeBlockers",
    "mergeStateStatus",
    "mergeable",
    "number",
    "participants",
    "reviewDecision",
    "repository",
    "reviewRequests",
    "state",
    "statusCheckRollup",
    "taskCount",
    "title",
    "unresolvedThreads",
    "updatedAt",
    "url",
    "version",
]


def nil_if_empty(xs: list) -> list | None:
    """Go marshals an unset slice as null."""
    return xs or None


def gh_state(pr: dict) -> str:
    """Bitbucket states in gh's vocabulary."""
    s = pr.get("state", "")
    return "CLOSED" if s == "DECLINED" else s


def review_state(status: str) -> str:
    return {"APPROVED": "APPROVED", "NEEDS_WORK": "CHANGES_REQUESTED"}.get(status, "PENDING")


def review_decision(pr: dict) -> str:
    reviewers = pr.get("reviewers") or []
    if not reviewers:
        return ""
    approved = False
    for r in reviewers:
        if r.get("status") == "NEEDS_WORK":
            return "CHANGES_REQUESTED"
        if r.get("status") == "APPROVED":
            approved = True
    return "APPROVED" if approved else "REVIEW_REQUIRED"


def merge_blockers(ms: dict) -> list[str]:
    """Why a PR can't be merged: conflicts and merge-check vetoes (required
    approvals, builds, open tasks, …). Empty when mergeable."""
    if ms.get("canMerge"):
        return []
    out = []
    if ms.get("conflicted"):
        out.append("the pull request has merge conflicts")
    for v in ms.get("vetoes") or []:
        msg = (v.get("summaryMessage") or "").strip()
        d = (v.get("detailedMessage") or "").strip()
        if d and d != msg:
            msg = msg.removesuffix(".") + ": " + d
        out.append(msg)
    if not out:
        out.append("Bitbucket reports the pull request as not mergeable")
    return out


def merge_state_status(pr: dict, ms: dict | None) -> str:
    """Bitbucket's merge status in gh's vocabulary; ms None: unknown."""
    if ms is None or pr.get("state") != "OPEN":
        return "UNKNOWN"
    if ms.get("conflicted"):
        return "DIRTY"
    if ms.get("canMerge"):
        return "CLEAN"
    if pr.get("draft"):
        return "DRAFT"
    return "BLOCKED"


def user_json(u: dict) -> dict:
    return {"login": u.get("slug", ""), "name": u.get("displayName", ""), "email": u.get("emailAddress", "")}


def comment_json(cm: dict) -> dict:
    m = {
        "id": cm.get("id", 0),
        "author": user_json(cm.get("author") or {}),
        "body": cm.get("text", ""),
        "createdAt": ts(cm.get("createdDate")),
        "updatedAt": ts(cm.get("updatedDate")),
        "isTask": cm.get("severity") == "BLOCKER",
        "state": cm.get("state", ""),
        "threadResolved": bool(cm.get("threadResolved")),
    }
    a = cm.get("anchor")
    if a is not None:
        m.update(
            path=a.get("path", ""),
            line=a.get("line", 0),
            lineType=a.get("lineType", ""),
            fileType=a.get("fileType", ""),
            orphaned=bool(a.get("orphaned")),
        )
    m["replies"] = nil_if_empty([comment_json(r) for r in cm.get("comments") or []])
    return m


def pr_json(c: Client, r: Repo, pr: dict, e: Exporter) -> dict:
    """The --json view. Fields needing extra API calls are only fetched when requested."""
    from .pr_review import build_threads

    reviewers = pr.get("reviewers") or []
    head_repo = base_repo = None
    hr, br = ref(pr, "fromRef").get("repository"), ref(pr, "toRef").get("repository")
    if hr is not None:
        head_repo = {"project": (hr.get("project") or {}).get("key", ""), "slug": hr.get("slug", ""), "name": hr.get("name", "")}
    if br is not None:
        base_repo = {
            "project": (br.get("project") or {}).get("key", ""),
            "slug": br.get("slug", ""),
            "name": br.get("name", ""),
            "nameWithOwner": bitbucket.full_name(br),
        }
    props = pr.get("properties") or {}
    pid = pr.get("id", 0)
    m = {
        "id": pid,
        "number": pid,
        "version": pr.get("version", 0),
        "title": pr.get("title", ""),
        "body": pr.get("description", ""),
        "state": gh_state(pr),
        "isDraft": bool(pr.get("draft")),
        "closed": bool(pr.get("closed")),
        "closedAt": ts(pr.get("closedDate")),
        "createdAt": ts(pr.get("createdDate")),
        "updatedAt": ts(pr.get("updatedDate")),
        "url": web_url(pr),
        "author": user_json(user_of(pr.get("author"))),
        "headRefName": ref(pr, "fromRef").get("displayId", ""),
        "headRefOid": ref(pr, "fromRef").get("latestCommit", ""),
        "baseRefName": ref(pr, "toRef").get("displayId", ""),
        "baseRefOid": ref(pr, "toRef").get("latestCommit", ""),
        "headRepository": head_repo,
        "repository": base_repo,
        "isCrossRepository": bitbucket.is_cross_repository(pr),
        "reviewRequests": nil_if_empty([user_json(user_of(rv)) for rv in reviewers]),
        "latestReviews": nil_if_empty(
            [{"author": user_json(user_of(rv)), "state": review_state(rv.get("status", ""))} for rv in reviewers]
        ),
        "participants": nil_if_empty(
            [
                {"user": user_json(user_of(p)), "role": p.get("role", ""), "status": p.get("status", "")}
                for p in pr.get("participants") or []
            ]
        ),
        "reviewDecision": review_decision(pr),
        "commentCount": props.get("commentCount", 0),
        "taskCount": props.get("openTaskCount", 0),
    }
    if e.wants("comments"):
        m["comments"] = nil_if_empty([comment_json(cm) for cm in c.comments(r.project, r.slug, pid)])
    if e.wants("commits"):
        m["commits"] = [
            {
                "oid": cm.get("id", ""),
                "messageHeadline": cm.get("message", "").split("\n", 1)[0],
                "message": cm.get("message", ""),
                "author": (cm.get("author") or {}).get("name", ""),
                "authoredDate": ts(cm.get("authorTimestamp")),
            }
            for cm in c.pr_commits(r.project, r.slug, pid)
        ]
    if e.wants("unresolvedThreads"):
        m["unresolvedThreads"] = sum(1 for t in build_threads(c, r, pr) if t.open())
    if e.wants("files"):
        files = []
        for f in c.changes(r.project, r.slug, pid):
            entry = {"path": (f.get("path") or {}).get("toString", ""), "changeType": f.get("type", "")}
            if f.get("srcPath") is not None:
                entry["previousPath"] = f["srcPath"].get("toString", "")
            files.append(entry)
        m["files"] = nil_if_empty(files)
    if e.wants("mergeable") or e.wants("mergeStateStatus") or e.wants("mergeBlockers"):
        try:
            ms: dict | None = c.merge_status(r.project, r.slug, pid)
        except Error:
            ms = None
        if ms is None:
            m["mergeable"] = "UNKNOWN"
        elif ms.get("conflicted"):
            m["mergeable"] = "CONFLICTING"
        else:
            m["mergeable"] = "MERGEABLE"
        m["mergeStateStatus"] = merge_state_status(pr, ms)
        m["mergeBlockers"] = merge_blockers(ms) if ms is not None and pr.get("state") == "OPEN" else []
    if e.wants("statusCheckRollup"):
        m["statusCheckRollup"] = nil_if_empty(
            [
                {
                    "name": b.get("name", ""),
                    "key": b.get("key", ""),
                    "state": b.get("state", ""),
                    "url": b.get("url", ""),
                    "description": b.get("description", ""),
                    "startedAt": ts(b.get("dateAdded")),
                }
                for b in c.build_statuses(ref(pr, "fromRef").get("latestCommit", ""))
            ]
        )
    return pick(m, e.fields)


def new_pr_list_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        if o.web:
            repo = c.get_repo(r.project, r.slug)
            output.open_browser(web_url(repo).removesuffix("/browse") + "/pull-requests")
            return
        bb_state = {"open": "OPEN", "closed": "DECLINED", "merged": "MERGED", "all": "ALL"}.get(o.state.lower(), "")
        if not bb_state:
            raise Error(f'invalid --state "{o.state}": valid values are {{open|closed|merged|all}}')
        q = {"state": bb_state, "order": "NEWEST"}
        if o.search:
            q["filterText"] = o.search
        if o.base:
            q["at"], q["direction"] = "refs/heads/" + o.base, "INCOMING"
        elif o.head:
            q["at"], q["direction"] = "refs/heads/" + o.head, "OUTGOING"
        n = 1
        for role, who in (("AUTHOR", o.author), ("REVIEWER", o.reviewer)):
            if not who:
                continue
            u = c.resolve_user(me_or(c, who))
            q[f"username.{n}"], q[f"role.{n}"] = u.get("name", ""), role
            n += 1

        def keep(p: dict) -> bool:
            if o.head and ref(p, "fromRef").get("displayId") != o.head:
                return False
            if o.base and ref(p, "toRef").get("displayId") != o.base:
                return False
            return not o.draft or bool(p.get("draft"))

        prs = c.paginate(r.path() + "/pull-requests", q, o.limit, keep)
        if exp.enabled():
            exp.write([pr_json(c, r, p, exp) for p in prs])
            return
        if not prs:
            raise ExitError(1, f"no pull requests match your search in {r.full_name()}")
        print_pr_table(prs, r)

    cmd = Command(
        "list",
        short="List pull requests in a repository",
        aliases=["ls"],
        example="""  $ bb pr list --author @me
  $ bb pr list --reviewer @me --state open
  $ bb pr list --json number,title,headRefName --jq '.[] | select(.title | test("fix"))'""",
        args=no_args,
        run=run,
    )
    cmd.flag("state", "s", default="open", help="Filter by state: {open|closed|merged|all}")
    cmd.flag("limit", "L", kind="int", default=30, help="Maximum number of items to fetch")
    cmd.flag("author", "A", help='Filter by author ("@me" for yourself)')
    cmd.flag("reviewer", help='Filter by reviewer ("@me" for yourself)')
    cmd.flag("base", "B", help="Filter by base branch")
    cmd.flag("head", "H", help="Filter by head branch")
    cmd.flag("search", "S", help="Filter by text in title or description")
    cmd.flag("draft", "d", kind="bool", help="Filter by draft state")
    cmd.flag("web", "w", kind="bool", help="List pull requests in the web browser")
    exp = add_json_flags(cmd, PR_FIELDS)
    return cmd


def print_pr_table(prs: list[dict], r: Repo) -> None:
    if not output.io.is_tty():
        for p in prs:
            head = ref(p, "fromRef").get("displayId", "")
            output.out(f"{p.get('id', 0)}\t{p.get('title', '')}\t{head}\t{gh_state(p)}\t{ts(p.get('createdDate'))}")
        return
    output.out(f"\nShowing {len(prs)} pull requests in {r.full_name()}\n")
    tw = TabWriter()
    tw.line("ID", "TITLE", "BRANCH", "CREATED AT")
    for p in prs:
        pid = f"#{p.get('id', 0)}"
        if p.get("draft"):
            pid = gray(pid)
        elif p.get("state") == "OPEN":
            pid = green(pid)
        elif p.get("state") == "MERGED":
            pid = magenta(pid)
        else:
            pid = red(pid)
        tw.line(pid, truncate(p.get("title", ""), 60), cyan(ref(p, "fromRef").get("displayId", "")), gray(ago(p.get("createdDate"))))
    tw.flush()


def reviewer_summary(pr: dict) -> str:
    parts = []
    for r in pr.get("reviewers") or []:
        st = {"APPROVED": "Approved", "NEEDS_WORK": "Changes requested"}.get(r.get("status", ""), "Requested")
        parts.append(f"{user_of(r).get('slug', '')} ({st})")
    return ", ".join(parts)


def new_pr_view_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        if o.web:
            output.open_browser(web_url(pr))
            return
        if exp.enabled():
            exp.write(pr_json(c, r, pr, exp))
            return
        cs = c.comments(r.project, r.slug, pr.get("id", 0)) if o.comments else []
        ms = None
        if pr.get("state") == "OPEN":
            try:
                ms = c.merge_status(r.project, r.slug, pr.get("id", 0))  # best effort; view works without it
            except Error:
                ms = None
        print_pr(pr, ms, cs, o.comments)

    cmd = Command(
        "view [<number> | <url> | <branch>]",
        short="View a pull request",
        long="""Display the title, body, and other information about a pull request.

With --comments, all comment threads are shown including inline comments
(file:line anchors) and comment IDs usable with "bb pr comment --reply-to".""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("comments", "c", kind="bool", help="View pull request comments")
    cmd.flag("web", "w", kind="bool", help="Open a pull request in the browser")
    exp = add_json_flags(cmd, PR_FIELDS)
    return cmd


def merge_line(ms: dict | None) -> str:
    """Mergeability for pr view; "" when unknown."""
    if ms is None:
        return ""
    if ms.get("canMerge"):
        return "ready"
    return "blocked: " + "; ".join(merge_blockers(ms))


def print_pr(pr: dict, ms: dict | None, cs: list[dict], with_comments: bool) -> None:
    props = pr.get("properties") or {}
    if not output.io.is_tty():
        output.out(f"title:\t{pr.get('title', '')}")
        output.out(f"state:\t{gh_state(pr)}")
        output.out(f"author:\t{user_of(pr.get('author')).get('slug', '')}")
        output.out(f"reviewers:\t{reviewer_summary(pr)}")
        output.out(f"number:\t{pr.get('id', 0)}")
        output.out(f"url:\t{web_url(pr)}")
        output.out(f"head:\t{ref(pr, 'fromRef').get('displayId', '')}")
        output.out(f"base:\t{ref(pr, 'toRef').get('displayId', '')}")
        output.out(f"draft:\t{'true' if pr.get('draft') else 'false'}")
        output.out(f"comments:\t{props.get('commentCount', 0)}")
        output.out(f"open tasks:\t{props.get('openTaskCount', 0)}")
        ml = merge_line(ms)
        if ml:
            output.out(f"merge:\t{ml}")
        output.out("--")
        output.out(pr.get("description", ""))
        if with_comments:
            output.out("--")
            print_comments(cs)
        return
    state = gh_state(pr)
    if pr.get("draft"):
        state = gray("Draft")
    elif state == "OPEN":
        state = green("Open")
    elif state == "MERGED":
        state = magenta("Merged")
    else:
        state = red("Closed")
    output.out(f"{bold(pr.get('title', ''))} {gray('#' + str(pr.get('id', 0)))}")
    output.out(
        f"{state} • {user_of(pr.get('author')).get('slug', '')} wants to merge into {cyan(ref(pr, 'toRef').get('displayId', ''))} "
        f"from {cyan(ref(pr, 'fromRef').get('displayId', ''))} • {gray(ago(pr.get('createdDate')))}"
    )
    rs = reviewer_summary(pr)
    if rs:
        output.out(f"{bold('Reviewers:')} {rs}")
    output.out(f"{bold('Activity:')} {props.get('commentCount', 0)} comments, {props.get('openTaskCount', 0)} open tasks")
    if ms is not None:
        if ms.get("canMerge"):
            output.out(f"{bold('Merge:')} {green('✓ Ready to merge')}")
        else:
            output.out(f"{bold('Merge:')} {red('✗ Blocked')}")
            for b in merge_blockers(ms):
                output.out(f"  - {b}")
    output.out()
    if not pr.get("description", "").strip():
        output.out(gray("No description provided"))
    else:
        output.out(pr.get("description", ""))
    if with_comments:
        output.out("\n" + bold("Comments"))
        print_comments(cs)
    output.out(gray("\nView this pull request on Bitbucket: " + web_url(pr)))


def print_comments(cs: list[dict]) -> None:
    if not cs:
        output.out("(no comments)")
    for cm in cs:
        print_comment(cm, 0)
        output.out()


def print_comment(cm: dict, depth: int) -> None:
    indent = "  " * depth
    head = [bold((cm.get("author") or {}).get("slug", "")), ts(cm.get("createdDate")), f"id:{cm.get('id', 0)}"]
    a = cm.get("anchor")
    if a is not None and a.get("path"):
        loc = a["path"]
        if a.get("line", 0) > 0:
            loc += f":{a['line']} ({a.get('lineType', '')})"
        if a.get("orphaned"):
            loc += " [outdated]"
        head.append(cyan(loc))
    if cm.get("severity") == "BLOCKER":
        head.append(yellow("[task " + cm.get("state", "").lower() + "]"))
    if cm.get("threadResolved"):
        head.append(gray("[resolved]"))
    prefix = "↳ " if depth > 0 else ""
    output.out(f"{indent}{prefix}{' • '.join(head)}")
    for line in cm.get("text", "").split("\n"):
        output.out(f"{indent}  {line}")
    for r in cm.get("comments") or []:
        print_comment(r, depth + 1)


def new_pr_status_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        me = c.current_user()
        current = None
        if r.remote:
            try:
                current = resolve_pr(c, r, "")
            except Error:
                current = None

        def by_role(role: str) -> list[dict]:
            u = c.resolve_user(me)
            return c.paginate(r.path() + "/pull-requests", {"state": "OPEN", "role.1": role, "username.1": u.get("name", "")}, 30)

        created = by_role("AUTHOR")
        review = by_role("REVIEWER")
        if exp.enabled():
            exp.write(
                {
                    "currentBranch": pr_json(c, r, current, exp) if current is not None else None,
                    "createdBy": [pr_json(c, r, p, exp) for p in created],
                    "needsReview": [pr_json(c, r, p, exp) for p in review],
                }
            )
            return
        output.out(f"\nRelevant pull requests in {r.full_name()}\n")

        def section(title: str, prs: list[dict]) -> None:
            output.out(bold(title))
            if not prs:
                output.out(gray("  You have no pull requests here"))
            for p in prs:
                output.out(f"  {green('#' + str(p.get('id', 0)))}  {p.get('title', '')} [{cyan(ref(p, 'fromRef').get('displayId', ''))}]")
                d = review_decision(p)
                if d:
                    output.out(f"    - {d.lower().replace('_', ' ')}")
            output.out()

        section("Current branch", [current] if current is not None else [])
        section("Created by you", created)
        section("Requesting a code review from you", review)

    cmd = Command("status", short="Show status of relevant pull requests", args=no_args, run=run)
    exp = add_json_flags(cmd, PR_FIELDS)
    return cmd

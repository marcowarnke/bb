from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor

from .. import bitbucket, output
from ..bitbucket import Client
from ..cli import Command, Error, Opts, no_args
from ..output import add_json_flags, bold, gray, green, red
from .common import ago
from .pr import PR_FIELDS, merge_blockers, pr_json
from .resolve import Repo, client_for

# One block of `bb status`: (key, title, empty, role, participant status).
STATUS_SECTIONS = [
    ("authored", "Your pull requests", "You have no open pull requests", "AUTHOR", ""),
    ("reviewRequested", "Review requested", "Nothing to review", "REVIEWER", "UNAPPROVED"),
    ("waitingOnAuthor", "Waiting on author (you requested changes)", "None", "REVIEWER", "NEEDS_WORK"),
]


def new_status_cmd() -> Command:
    def run(o: Opts) -> None:
        c = client_for(o.hostname)
        results = []
        for _, _, _, role, participation in STATUS_SECTIONS:
            q = {"state": "OPEN", "role": role, "order": "NEWEST"}
            if participation:
                q["participantStatus"] = participation
            results.append(c.paginate("rest/api/1.0/dashboard/pull-requests", q, o.limit))
        if exp.enabled():
            exp.write({s[0]: [pr_json(c, pr_repo(pr), pr, exp) for pr in results[i]] for i, s in enumerate(STATUS_SECTIONS)})
            return
        merge = merge_statuses(c, results[0])
        for i, s in enumerate(STATUS_SECTIONS):
            print_status_section(s, results[i], merge)

    cmd = Command(
        "status",
        short="Print your pull request dashboard across all repositories",
        long="""Show open pull requests relevant to you across every repository on the host:

- Your pull requests, with whether each can be merged and what blocks it
  (approvals, builds, tasks, conflicts).
- Pull requests waiting for your review.
- Pull requests where you requested changes, waiting on the author.

With --json, the result is an object with "authored", "reviewRequested" and
"waitingOnAuthor" arrays of pull requests (same fields as "bb pr view", plus
"repository").""",
        example="""  $ bb status
  $ bb status --json number,title,repository,url --jq '.reviewRequested[].url'""",
        args=no_args,
        run=run,
    )
    cmd.flag("hostname", help="The Bitbucket `host` to use (default: BB_HOST or the only configured host)")
    cmd.flag("limit", "L", kind="int", default=30, help="Maximum number of pull requests per section")
    exp = add_json_flags(cmd, PR_FIELDS)
    return cmd


def pr_repo(pr: dict) -> Repo:
    """The repository a PR targets (dashboard PRs span repos)."""
    tr = bitbucket.ref(pr, "toRef").get("repository")
    if tr is not None:
        return Repo(project=(tr.get("project") or {}).get("key", ""), slug=tr.get("slug", ""))
    return Repo()


def merge_statuses(c: Client, prs: list[dict]) -> dict[int, dict]:
    """Merge status for each PR, concurrently; failures are left out (shown as unknown)."""

    def one(i: int) -> tuple[int, dict | None]:
        r = pr_repo(prs[i])
        try:
            return i, c.merge_status(r.project, r.slug, prs[i].get("id", 0))
        except Error:
            return i, None

    with ThreadPoolExecutor(8) as pool:
        return {i: ms for i, ms in pool.map(one, range(len(prs))) if ms is not None}


def status_note(s: tuple, i: int, pr: dict, merge: dict[int, dict]) -> str:
    """Merge readiness for your PRs, author and age for others'."""
    if s[3] != "AUTHOR":
        return f"by {bitbucket.user_of(pr.get('author')).get('slug', '')}, updated {ago(pr.get('updatedDate'))}"
    ms = merge.get(i)
    if ms is None:
        return "merge status unknown"
    if ms.get("canMerge") and pr.get("draft"):
        return "draft; ready to merge once published"
    if ms.get("canMerge"):
        return "ready to merge"
    return "blocked: " + "; ".join(merge_blockers(ms))


def print_status_section(s: tuple, prs: list[dict], merge: dict[int, dict]) -> None:
    if not output.io.is_tty():
        for i, pr in enumerate(prs):
            output.out(
                f"{s[0]}\t{pr_repo(pr).full_name()}\t{pr.get('id', 0)}\t{pr.get('title', '')}\t"
                f"{bitbucket.web_url(pr)}\t{status_note(s, i, pr, merge)}"
            )
        return
    output.out(bold(s[1]))
    if not prs:
        output.out(gray("  " + s[2]))
    for i, pr in enumerate(prs):
        ref = f"{pr_repo(pr).full_name()}#{pr.get('id', 0)}"
        ref = gray(ref) if pr.get("draft") else green(ref)
        output.out(f"  {ref}  {pr.get('title', '')}")
        note = status_note(s, i, pr, merge)
        if note.startswith("ready"):
            note = green("✓ " + note)
        elif note.startswith("blocked"):
            note = red("✗ ") + note
        else:
            note = gray(note)
        output.out(f"    {note}")
    output.out()

from __future__ import annotations

from .. import bitbucket, git, output
from ..bitbucket import Client, ref
from ..cli import Command, Error, ExitError, Opts, exact_args, maximum_args
from ..output import Exporter, TabWriter, add_json_flags, gray, green, pick, red, yellow
from . import common
from .common import arg_or, first_non_empty, short, ts
from .diffmodel import (
    NUMBERED_LEGEND,
    PathFilter,
    RawSection,
    diff_json,
    diff_stat,
    limit_sections,
    numbered,
    split_after_newline,
    split_raw_diff,
)
from .resolve import repo_client, resolve_pr


def new_pr_diff_cmd() -> Command:
    def run(o: Opts) -> None:
        args, paths = o.args, []
        if o.dash >= 0:
            args, paths = o.args[: o.dash], o.args[o.dash :]
        if len(args) > 1:
            raise Error('accepts at most one pull request argument; put paths after "--"')
        if o.color not in ("auto", "always", "never"):
            raise Error(f'invalid --color "{o.color}": valid values are {{always|never|auto}}')
        since = o.since
        if o.since_last_review and since:
            raise Error("--since and --since-last-review are mutually exclusive")
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, args[0] if args else "")
        if o.web:
            output.open_browser(bitbucket.web_url(pr) + "/diff")
            return
        flt = PathFilter(paths, o.exclude)
        if o.since_last_review:
            since = last_reviewed_commit(c, pr)
        head = ref(pr, "fromRef").get("latestCommit", "")
        if since:
            if head.startswith(since):
                output.err(f"No changes since {short(since)} (head is {short(head)}).")
                return
            output.err(f"Changes from {short(since)} to {short(head)} (head).")
        pid = pr.get("id", 0)

        if o.name_only and not since:
            for f in c.changes(r.project, r.slug, pid):
                old = (f.get("srcPath") or {}).get("toString", "") if f.get("srcPath") is not None else ""
                p = (f.get("path") or {}).get("toString", "")
                if flt.match(p, old):
                    output.out(p)
            return

        structured = o.line_numbers or o.json or o.stat or o.name_only or bool(since)
        if not structured:
            d = c.raw_diff(r.project, r.slug, pid, o.patch)
            secs = [s for s in split_raw_diff(d) if not s.path or flt.match(s.path, s.old_path)]
            print_sections(secs, "", o.color, o.max_lines)
            return

        ctx = 0 if (o.stat or o.name_only) else o.context
        if since:
            try:
                d = c.compare_diff(r.project, r.slug, head, since, "", ctx)
            except bitbucket.APIError as e:
                if e.status == 404:
                    raise Error(f"commit {short(since)} not found (rebased or force-pushed?); review the full diff instead") from e
                raise
        else:
            d = c.pr_diff(r.project, r.slug, pid, "", ctx)
        files = flt.diffs(d.get("diffs") or [])
        if d.get("truncated"):
            output.err('warning: the server truncated this diff; narrow it with paths after "--"')
        if o.name_only:
            for f in files:
                output.out(bitbucket.diff_path(f))
        elif o.stat:
            print_stat(files)
        elif o.json:
            out, omitted, used = [], [], 0
            for f in files:
                m = diff_json(f)
                n = sum(len(s.get("lines") or []) for h in f.get("hunks") or [] for s in h.get("segments") or [])
                if o.max_lines > 0 and used + n > o.max_lines and used > 0:
                    omitted.append(bitbucket.diff_path(f))
                    continue
                used += n
                out.append(m)
            report_omitted(omitted)
            Exporter().write({"headRefOid": head, "since": since, "truncated": bool(d.get("truncated")), "files": out})
        elif o.line_numbers:
            print_sections(
                [RawSection(path=bitbucket.diff_path(f), text=numbered(f)) for f in files], NUMBERED_LEGEND, "never", o.max_lines
            )
        else:
            print_sections(
                [RawSection(path=bitbucket.diff_path(f), text=bitbucket.unified_file(f)) for f in files], "", o.color, o.max_lines
            )

    cmd = Command(
        "diff [<number> | <url> | <branch>] [-- <path>...]",
        short="View changes in a pull request",
        long="""View changes in a pull request.

Agent-oriented options:
  -n, --line-numbers     print explicit OLD/NEW line numbers; use them verbatim
                         for "bb pr comment --line" and review files
  --json                 structured diff: files → hunks → lines with old/new numbers
  --stat                 per-file additions/deletions
  -- <path>...           only these paths (directory prefixes or globs, ** allowed)
  --exclude <glob>       skip paths, e.g. --exclude 'vendor/**,*.lock'
  --max-lines N          stop before exceeding N lines; omitted files are listed on stderr
  --since-last-review    only changes since the commit you last reviewed
  --since <commit>       only changes since <commit>

Line-numbered and JSON output use the server's default context, which is
exactly the set of lines Bitbucket accepts comments on.""",
        example="""  $ bb pr diff 42 --stat
  $ bb pr diff 42 -n -- src/api/
  $ bb pr diff 42 -n --exclude '**/*_test.go' --max-lines 1500
  $ bb pr diff 42 --since-last-review -n""",
        run=run,
    )
    cmd.flag("name-only", kind="bool", help="Display only names of changed files")
    cmd.flag("patch", kind="bool", help="Display diff in patch format")
    cmd.flag("web", "w", kind="bool", help="Open the pull request diff in the browser")
    cmd.flag("color", default="auto", help="Use color in diff output: {always|never|auto}")
    cmd.flag("line-numbers", "n", kind="bool", help="Show explicit old/new line numbers for commenting")
    cmd.flag("json", kind="bool", help="Output the diff as structured JSON")
    cmd.flag("stat", kind="bool", help="Show per-file additions and deletions")
    cmd.flag("exclude", kind="strs", help="Skip paths matching these `globs`")
    cmd.flag("max-lines", kind="int", help="Stop output before exceeding this many lines (0: unlimited)")
    cmd.flag("context", "U", kind="int", default=-1, help="Context lines for structured output (default: server default)")
    cmd.flag("since-last-review", kind="bool", help="Only show changes since the commit you last reviewed")
    cmd.flag("since", help="Only show changes since this `commit`")
    return cmd


def print_sections(secs: list[RawSection], legend: str, color: str, max_lines: int) -> None:
    """Write file sections, stopping before max_lines is exceeded."""
    out, omitted = limit_sections(secs, max_lines)
    if not out and not omitted:
        output.err("No changes match the given paths.")
        return
    if color == "always" or (color == "auto" and output.color_on()):
        out = colorize_diff(out)
    output.out(legend + out, end="")
    report_omitted(omitted)


def report_omitted(omitted: list[str]) -> None:
    if omitted:
        output.err(
            f"\n# --max-lines reached; {len(omitted)} file(s) not shown (request them with `-- <path>`):\n#   " + "\n#   ".join(omitted)
        )


def print_stat(files: list[dict]) -> None:
    ta = td = 0
    for f in files:
        a, d = diff_stat(f)
        ta, td = ta + a, td + d
        path = bitbucket.diff_path(f)
        if bitbucket.change_type(f) == "RENAME":
            path = bitbucket.diff_old_path(f) + " → " + path
        output.out(f"{'+' + str(a):>6} {'-' + str(d):>6}  {path}")
    output.out(f"{len(files)} files changed, +{ta} -{td}")


def last_reviewed_commit(c: Client, pr: dict) -> str:
    """The commit the current user last reviewed."""
    me = c.current_user()
    for p in (pr.get("reviewers") or []) + (pr.get("participants") or []):
        if bitbucket.user_of(p).get("slug") == me and p.get("lastReviewedCommit"):
            return p["lastReviewedCommit"]
    raise Error(f"no previous review by {me} recorded on #{pr.get('id', 0)}; use --since <commit> or review the full diff")


def colorize_diff(d: str) -> str:
    out = []
    for line in split_after_newline(d):
        code = ""
        if line.startswith(("+++", "---", "diff ")):
            code = "1"
        elif line.startswith("@@"):
            code = "36"
        elif line.startswith("+"):
            code = "32"
        elif line.startswith("-"):
            code = "31"
        if code:
            body = line.rstrip("\n")
            line = f"\x1b[{code}m{body}\x1b[0m" + line[len(body) :]
        out.append(line)
    return "".join(out)


def bucket(state: str) -> str:
    return {"SUCCESSFUL": "pass", "FAILED": "fail", "CANCELLED": "cancel"}.get(state, "pending")


def new_pr_checks_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        arg = arg_or(o)
        pr = resolve_pr(c, r, arg)
        if o.web:
            output.open_browser(bitbucket.web_url(pr) + "/builds")
            return
        if not arg:
            arg = str(pr.get("id", 0))  # keep following the same PR while watching
        waited = 0
        while True:
            bs = c.build_statuses(ref(pr, "fromRef").get("latestCommit", ""))
            pending = [first_non_empty(b.get("name", ""), b.get("key", "")) for b in bs if bucket(b.get("state", "")) == "pending"]
            if not o.watch or (bs and not pending) or (not bs and waited >= 120):
                break
            if not bs:
                output.err(f"waiting for checks on {short(ref(pr, 'fromRef').get('latestCommit', ''))}…")
            else:
                output.err(f"waiting: {len(pending)} pending ({', '.join(pending)})…")
            common.sleep(o.interval)
            waited += o.interval
            pr = resolve_pr(c, r, arg)
        if exp.enabled():
            exp.write(
                [
                    pick(
                        {
                            "name": b.get("name", ""),
                            "key": b.get("key", ""),
                            "state": b.get("state", ""),
                            "bucket": bucket(b.get("state", "")),
                            "link": b.get("url", ""),
                            "description": b.get("description", ""),
                            "startedAt": ts(b.get("dateAdded")),
                        },
                        exp.fields,
                    )
                    for b in bs
                ]
            )
        elif not bs:
            raise Error(f"no checks reported on the '{ref(pr, 'fromRef').get('displayId', '')}' branch")
        else:
            print_checks(bs)
        buckets = {bucket(b.get("state", "")) for b in bs}
        if "fail" in buckets:
            raise ExitError(1)
        if "pending" in buckets:
            raise ExitError(8)

    cmd = Command(
        "checks [<number> | <url> | <branch>]",
        short="Show build status for a pull request",
        long="""Show build statuses reported for the pull request's head commit.

With --watch, wait until no check is pending (re-reading the head commit,
so it follows new pushes). If no check has appeared after 2 minutes, it
gives up.

Exit codes: 0 all passed, 1 a check failed, 8 checks pending.""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("web", "w", kind="bool", help="Open the web browser to show build details")
    cmd.flag("watch", kind="bool", help="Wait until no check is pending")
    cmd.flag("interval", "i", kind="int", default=10, help="Refresh interval in seconds for --watch")
    exp = add_json_flags(cmd, ["bucket", "description", "key", "link", "name", "startedAt", "state"])
    return cmd


def print_checks(bs: list[dict]) -> None:
    if not output.io.is_tty():
        for b in bs:
            name = first_non_empty(b.get("name", ""), b.get("key", ""))
            output.out(f"{name}\t{bucket(b.get('state', ''))}\t{b.get('url', '')}\t{b.get('description', '')}")
        return
    tw = TabWriter()
    for b in bs:
        mark = {"pass": green("✓"), "fail": red("X"), "cancel": gray("-")}.get(bucket(b.get("state", "")), yellow("*"))
        tw.line(mark, first_non_empty(b.get("name", ""), b.get("key", "")), gray(b.get("description", "")), b.get("url", ""))
    tw.flush()


def new_pr_checkout_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, o.args[0])
        remote = r.remote
        if not remote:
            repo = c.get_repo(r.project, r.slug)
            remote = bitbucket.clone_url(repo, "ssh") or bitbucket.clone_url(repo, "http")
        head = ref(pr, "fromRef").get("displayId", "")
        local = first_non_empty(o.branch, head)
        cross = bitbucket.is_cross_repository(pr)
        src = f"refs/pull-requests/{pr.get('id', 0)}/from" if cross or not r.remote else "refs/heads/" + head
        tracking = f"refs/remotes/{r.remote}/{head}"
        if not r.remote or cross:
            tracking = "refs/heads/" + local
            try:
                if git.current_branch() == local:
                    tracking = ""
            except Error:
                pass
        fetch_args = ["fetch", remote]
        if tracking:
            refspec = src + ":" + tracking
            if o.force or tracking.startswith("refs/remotes/"):
                refspec = "+" + refspec
            fetch_args.append(refspec)
        else:
            fetch_args.append(src)
        git.passthrough(*fetch_args)
        if not tracking:
            git.passthrough("merge", "--ff-only", "FETCH_HEAD")
        elif tracking.startswith("refs/heads/"):
            git.passthrough("checkout", local)
        elif git.has_local_branch(local):
            git.passthrough("checkout", local)
            if o.force:
                git.passthrough("reset", "--hard", tracking)
            else:
                git.passthrough("merge", "--ff-only", tracking)
        else:
            git.passthrough("checkout", "-b", local, "--track", f"{r.remote}/{head}")

    cmd = Command(
        "checkout {<number> | <url> | <branch>}", short="Check out a pull request in git", aliases=["co"], args=exact_args(1), run=run
    )
    cmd.flag("branch", "b", help="Local branch name to use (default [the name of the head branch])")
    cmd.flag("force", "f", kind="bool", help="Reset the existing local branch to the latest state of the pull request")
    return cmd

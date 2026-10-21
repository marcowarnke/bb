from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any

from .. import output
from ..bitbucket import Client, path_escape, ref, web_url
from ..cli import Command, Error, Opts, maximum_args
from ..output import add_json_flags, bold, cyan, gray, pick, success, yellow
from .common import arg_or, read_body, short, ts
from .diffmodel import CommentSpec, DiffCache
from .resolve import Repo, me_or, repo_client, resolve_pr


def comment_url(pr: dict, cid: int) -> str:
    return f"{web_url(pr)}/overview?commentId={cid}"


def post_comment(c: Client, r: Repo, pr: dict, payload: dict) -> dict:
    return c.do("POST", r.pr_path(pr.get("id", 0)) + "/comments", None, payload) or {}


def new_pr_comment_cmd() -> Command:
    def run(o: Opts) -> None:
        body, _ = read_body(o, o.body, o.body_file)
        spec = CommentSpec(path=o.path, line=o.line, lineType=o.line_type, body=body, task=o.task, replyTo=o.reply_to)
        if o.changed("suggestion"):
            spec.suggestion = o.suggestion
        if o.resolve and o.reopen:
            raise Error("--resolve and --reopen are mutually exclusive")
        if (o.resolve or o.reopen) and spec.replyTo == 0:
            raise Error("--resolve/--reopen need --reply-to <comment-id>")
        post = bool(spec.body.strip()) or spec.suggestion is not None
        if not post and not o.resolve and not o.reopen:
            raise Error("comment body required: use --body or --body-file")
        if spec.replyTo > 0 and spec.path:
            raise Error("--reply-to cannot be combined with --path (replies inherit the thread's location)")
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        if post:
            payload = DiffCache(c, r, pr.get("id", 0)).comment_payload(spec)
            cm = post_comment(c, r, pr, payload)
            output.out(comment_url(pr, cm.get("id", 0)))
        if o.resolve or o.reopen:
            what = set_resolved(c, r, pr, spec.replyTo, o.resolve)
            verb = "Reopened" if o.reopen else "Resolved"
            success(f"{verb} {what} on {r.full_name()}#{pr.get('id', 0)}")

    cmd = Command(
        "comment [<number> | <url> | <branch>]",
        short="Add a comment to a pull request",
        long="""Add a comment to a pull request.

Beyond gh, inline and threaded comments are supported:
  --path FILE               comment on a file
  --path FILE --line N      comment on a line. N is the NEW-file line number; the
                            line type (added/context) is inferred from the diff.
                            The line is validated before posting; on failure the
                            commentable lines are listed.
  --line-type removed       N is an OLD-file line number of a removed line
  --suggestion CODE         propose replacement code for the line (renders as an
                            applicable suggestion on recent Bitbucket versions)
  --task                    create the comment as a blocking task
  --reply-to ID             reply to a comment (IDs: bb pr threads)
  --resolve / --reopen      with --reply-to: resolve or reopen that task or
                            thread; the body is optional

Line numbers are easiest to take from "bb pr diff <pr> -n".
The comment URL is printed on success.""",
        example="""  $ bb pr comment 12 --body "Looks good overall"
  $ bb pr comment 12 -b "Off-by-one?" --path pkg/x.go --line 42
  $ bb pr comment 12 -b "Use the constant" --path pkg/x.go --line 42 --suggestion '	return maxRetries'
  $ bb pr comment 12 -b "Old code was safer" --path pkg/x.go --line 40 --line-type removed
  $ bb pr comment 12 --reply-to 1234 -b "Fixed in 3f2a1c9" --resolve
  $ git diff | bb pr comment 12 -F -""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("body", "b", help="The comment body `text`")
    cmd.flag("body-file", "F", help='Read body text from `file` (use "-" to read from standard input)')
    cmd.flag("path", help="File path for an inline comment")
    cmd.flag("line", kind="int", help="Line number for an inline comment (new file; old file with --line-type removed)")
    cmd.flag("line-type", help="Line type: {added|removed|context} (default: inferred from the diff)")
    cmd.flag("suggestion", help="Suggested replacement `code` for the commented line")
    cmd.flag("reply-to", kind="int", help="Reply to the comment with this `id`")
    cmd.flag("task", kind="bool", help="Create the comment as a blocking task")
    cmd.flag("resolve", kind="bool", help="Resolve the task or thread given by --reply-to")
    cmd.flag("reopen", kind="bool", help="Reopen the task or thread given by --reply-to")
    return cmd


def set_resolved(c: Client, r: Repo, pr: dict, cid: int, resolved: bool) -> str:
    """Resolve/reopen a task (by its own state) or a thread (on its root
    comment, so a reply ID works too)."""
    roots = c.comments(r.project, r.slug, pr.get("id", 0))

    def find(cs: list[dict], top: dict | None) -> tuple[dict, dict] | None:
        for cm in cs or []:
            t = top if top is not None else cm
            if cm.get("id") == cid:
                return cm, t
            hit = find(cm.get("comments") or [], t)
            if hit:
                return hit
        return None

    hit = find(roots, None)
    if hit is None:
        raise Error(f"comment {cid} not found on {r.full_name()}#{pr.get('id', 0)}")
    target, root = hit
    is_task = target.get("severity") == "BLOCKER"
    if not is_task:
        target = root
    fresh = c.get_comment(r.project, r.slug, pr.get("id", 0), target.get("id", 0))
    payload: dict[str, Any] = {"version": fresh.get("version", 0)}
    if is_task:
        payload["state"] = "RESOLVED" if resolved else "OPEN"
        what = f"task {target.get('id', 0)}"
    else:
        payload["threadResolved"] = resolved
        what = f"thread {target.get('id', 0)}"
    c.do("PUT", f"{r.pr_path(pr.get('id', 0))}/comments/{target.get('id', 0)}", None, payload)
    return what


# The --input format for `bb pr review`, modelled on GitHub's review API.
REVIEW_FIELDS = {"event": str, "body": str, "comments": list}
SPEC_FIELDS = {"path": str, "line": int, "lineType": str, "body": str, "task": bool, "suggestion": str, "replyTo": int}
GO_TYPES = {str: "string", int: "int", bool: "bool", list: "[]cmd.commentSpec"}


def parse_review_input(data: str) -> tuple[str, str, list[CommentSpec]]:
    """Decode like Go's json.Decoder with DisallowUnknownFields (field names match case-insensitively)."""
    try:
        raw, _ = json.JSONDecoder().raw_decode(data.lstrip())
    except ValueError as e:
        raise Error(f"parsing review input: {e}") from None

    def fields(obj: Any, spec: dict, where: str) -> dict:
        if obj is None:
            return {}
        if not isinstance(obj, dict):
            raise Error(f"parsing review input: json: cannot unmarshal {_json_type(obj)} into Go value of type {where}")
        out = {}
        for k, v in obj.items():
            name = next((f for f in spec if f.lower() == k.lower()), None)
            if name is None:
                raise Error(f'parsing review input: json: unknown field "{k}"')
            typ = spec[name]
            if v is None:
                continue
            ok = (isinstance(v, typ) and not (typ is int and isinstance(v, bool))) or (
                typ is int and isinstance(v, float) and v.is_integer()
            )
            if not ok:
                field_type = f"{where}.{name} of type {GO_TYPES[typ]}"
                raise Error(f"parsing review input: json: cannot unmarshal {_json_type(v)} into Go struct field {field_type}")
            out[name] = int(v) if typ is int else v
        return out

    top = fields(raw, REVIEW_FIELDS, "cmd.reviewFile")
    specs = []
    for item in top.get("comments") or []:
        f = fields(item, SPEC_FIELDS, "reviewFile.comments")
        specs.append(CommentSpec(**f))
    return top.get("event", ""), top.get("body", ""), specs


def _json_type(v: Any) -> str:
    if isinstance(v, bool):
        return "bool"
    if isinstance(v, (int, float)):
        return "number"
    if isinstance(v, str):
        return "string"
    if isinstance(v, list):
        return "array"
    return "object"


def new_pr_review_cmd() -> Command:
    def run(o: Opts) -> None:
        if sum((o.approve, o.request_changes, o.comment)) > 1:
            raise Error("only one of --approve, --request-changes, or --comment can be used")
        event, rbody, specs = "", "", []
        if o.input:
            if o.input == "-":
                data = output.read_stdin()
            else:
                with open(o.input, encoding="utf-8") as f:
                    data = f.read()
            event, rbody, specs = parse_review_input(data)
        if o.approve:
            event = "APPROVE"
        elif o.request_changes:
            event = "REQUEST_CHANGES"
        elif o.comment:
            event = "COMMENT"
        elif not event and o.input:
            event = "COMMENT"
        event = event.upper()
        if event not in ("APPROVE", "REQUEST_CHANGES", "COMMENT"):
            raise Error('need one of --approve, --request-changes, --comment (or "event" in --input)')
        b, given = read_body(o, o.body, o.body_file)
        if given:
            rbody = b
        if event == "COMMENT" and not rbody.strip() and not specs:
            raise Error("body cannot be blank for comment review")
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))

        dc = DiffCache(c, r, pr.get("id", 0))
        payloads: list[dict] = []
        problems = []
        for i, s in enumerate(specs):
            try:
                payloads.append(dc.comment_payload(s))
            except Error as e:
                problems.append(f"comments[{i}] ({s.location()}): {e}")
        if problems:
            raise Error(f"review not submitted, {len(problems)} of {len(specs)} comments are invalid:\n" + "\n".join(problems))
        tasks = sum(1 for s in specs if s.task)
        ref_name = f"{r.full_name()}#{pr.get('id', 0)}"
        if o.dry_run:
            for s in specs:
                output.out(f"{s.location()}\t{'task' if s.task else 'comment'}")
            success(f"Review for {ref_name} is valid: {event}, {len(specs)} comments ({tasks} tasks); nothing posted")
            return

        for i, p in enumerate(payloads):
            try:
                cm = post_comment(c, r, pr, p)
            except Error as e:
                raise Error(
                    f"posting comments[{i}] ({specs[i].location()}) failed: {e}\ncomments[0..{i - 1}] were posted; "
                    f"resubmit only comments[{i}:] (without the summary/event if already sent)"
                ) from e
            output.out(f"{specs[i].location()}\t{comment_url(pr, cm.get('id', 0))}")
        if rbody.strip():
            try:
                cm = post_comment(c, r, pr, {"text": rbody})
            except Error as e:
                raise Error(f"posting summary failed (all {len(payloads)} inline comments were posted): {e}") from e
            output.out(f"summary\t{comment_url(pr, cm.get('id', 0))}")
        if event != "COMMENT":
            me = c.current_user()
            status = "NEEDS_WORK" if event == "REQUEST_CHANGES" else "APPROVED"
            c.do(
                "PUT",
                f"{r.pr_path(pr.get('id', 0))}/participants/{path_escape(me)}",
                None,
                {"user": {"name": me}, "approved": status == "APPROVED", "status": status},
            )
        verb = {"APPROVE": "Approved", "REQUEST_CHANGES": "Requested changes to", "COMMENT": "Reviewed"}[event]
        success(
            f"{verb} pull request {ref_name} at {short(ref(pr, 'fromRef').get('latestCommit', ''))} ({len(specs)} comments, {tasks} tasks)"
        )

    cmd = Command(
        "review [<number> | <url> | <branch>]",
        short="Add a review to a pull request",
        long="""Add a review to a pull request.

--approve sets your status to Approved, --request-changes to Needs work.
A body, if given, is posted as a general (summary) comment.

--input FILE (or "-" for stdin) submits a whole review in one step:

  {
    "event": "REQUEST_CHANGES",            // APPROVE | REQUEST_CHANGES | COMMENT
    "body": "Summary of the review",
    "comments": [
      {"path": "src/a.go", "line": 42, "body": "Nil deref if cfg is missing", "task": true},
      {"path": "src/a.go", "line": 50, "body": "Prefer the constant", "suggestion": "\\treturn maxRetries"},
      {"path": "src/old.go", "line": 7, "lineType": "removed", "body": "Was this intended?"},
      {"path": "docs/x.md", "body": "File-level comment"},
      {"body": "General comment"}
    ]
  }

Line numbers follow "bb pr comment": new-file numbers, or old-file numbers
with "lineType": "removed" (take them from "bb pr diff <pr> -n").
"task": true makes a blocking task the author must resolve.

Every comment is validated against the diff first; if any is invalid,
nothing is posted and all problems are listed. --dry-run only validates.
Comments are posted, then the summary, then the status. Each posted comment
is printed as "<location>\\t<url>".""",
        example="""  $ bb pr review --approve
  $ bb pr review --comment -b "interesting"
  $ bb pr review 12 -r -b "needs more ASCII art"
  $ bb pr review 12 --input review.json --dry-run && bb pr review 12 --input review.json""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("approve", "a", kind="bool", help="Approve pull request")
    cmd.flag("request-changes", "r", kind="bool", help="Request changes on a pull request (Needs work)")
    cmd.flag("comment", "c", kind="bool", help="Comment on a pull request")
    cmd.flag("body", "b", help="Specify the body of a review")
    cmd.flag("body-file", "F", help='Read body text from `file` (use "-" to read from standard input)')
    cmd.flag("input", help='Submit a whole review from a JSON `file` ("-" for stdin)')
    cmd.flag("dry-run", kind="bool", help="Validate the review without posting anything")
    return cmd


@dataclass
class Thread:
    """One item of feedback: a comment thread or a task, flattened for agents."""

    id: int
    kind: str  # "task" or "thread"
    state: str
    author: str
    body: str
    created_at: str
    url: str
    path: str = ""
    line: int = 0
    line_type: str = ""
    outdated: bool = False
    replies: list[dict] = field(default_factory=list)
    open_tasks: list[dict] = field(default_factory=list)
    last_by: str = ""
    needs_reply: bool = False
    context: list[str] = field(default_factory=list)
    anchor: dict | None = None

    def open(self) -> bool:
        return self.state == "open" or bool(self.open_tasks)

    def to_json(self) -> dict:
        """The Go struct's JSON (omitempty: path, line, lineType, context)."""
        m: dict[str, Any] = {"id": self.id, "kind": self.kind, "state": self.state}
        if self.path:
            m["path"] = self.path
        if self.line:
            m["line"] = self.line
        if self.line_type:
            m["lineType"] = self.line_type
        m.update(
            outdated=self.outdated,
            author=self.author,
            body=self.body,
            createdAt=self.created_at,
            replies=self.replies,
            openTasks=self.open_tasks,
            lastCommentBy=self.last_by,
            needsReply=self.needs_reply,
            url=self.url,
        )
        if self.context:
            m["context"] = self.context
        return m


def build_threads(c: Client, r: Repo, pr: dict) -> list[Thread]:
    """The PR's comments flattened into threads, oldest first."""
    roots = c.comments(r.project, r.slug, pr.get("id", 0))
    me = c.current_user()
    out = []
    for root in roots:
        t = Thread(
            id=root.get("id", 0),
            kind="thread",
            state="open",
            author=(root.get("author") or {}).get("slug", ""),
            body=root.get("text", ""),
            created_at=ts(root.get("createdDate")),
            url=comment_url(pr, root.get("id", 0)),
            anchor=root.get("anchor"),
        )
        if root.get("severity") == "BLOCKER":
            t.kind = "task"
            if root.get("state") == "RESOLVED":
                t.state = "resolved"
        elif root.get("threadResolved"):
            t.state = "resolved"
        a = root.get("anchor")
        if a is not None:
            t.path, t.line, t.line_type, t.outdated = (
                a.get("path", ""),
                a.get("line", 0),
                a.get("lineType", "").lower(),
                bool(a.get("orphaned")),
            )
        last = _walk_replies(t, root.get("comments") or [], root)
        t.replies.sort(key=lambda x: x["createdAt"])
        t.last_by = (last.get("author") or {}).get("slug", "")
        t.needs_reply = t.last_by != me
        out.append(t)
    return out


def _walk_replies(t: Thread, cs: list[dict], last: dict) -> dict:
    """Collect the replies (and open tasks among them) under a thread; returns the latest comment."""
    for cm in cs:
        is_task = cm.get("severity") == "BLOCKER"
        t.replies.append(
            {
                "id": cm.get("id", 0),
                "author": (cm.get("author") or {}).get("slug", ""),
                "body": cm.get("text", ""),
                "createdAt": ts(cm.get("createdDate")),
                "isTask": is_task,
                "state": cm.get("state", "").lower(),
            }
        )
        if is_task and cm.get("state") == "OPEN":
            t.open_tasks.append({"id": cm.get("id", 0), "body": cm.get("text", "")})
        if (cm.get("createdDate") or 0) >= (last.get("createdDate") or 0):
            last = cm
        last = _walk_replies(t, cm.get("comments") or [], last)
    return last


def add_context(c: Client, r: Repo, pr: dict, items: list[Thread], n: int) -> None:
    """Attach the code the comment was made on, from the commit the anchor
    refers to (falling back to the PR head for new-file lines)."""
    cache: dict[str, list[str]] = {}
    for t in items:
        a = t.anchor
        if n <= 0 or a is None or a.get("line", 0) <= 0 or a.get("orphaned"):
            continue
        at, path = a.get("toHash", ""), a.get("path", "")
        if a.get("fileType") == "FROM":
            at = a.get("fromHash", "")
            if a.get("srcPath"):
                path = a["srcPath"]
        if not at and a.get("fileType") != "FROM":
            at = ref(pr, "fromRef").get("latestCommit", "")
        if not at:
            continue
        key = at + ":" + path
        if key not in cache:
            try:
                cache[key] = c.raw_file(r.project, r.slug, path, at).split("\n")
            except Error:
                cache[key] = []
        lines = cache[key]
        if not lines:
            continue
        line = a["line"]
        for ln in range(line - n, line + n + 1):
            if 1 <= ln <= len(lines):
                mark = ">" if ln == line else " "
                t.context.append(f"{mark}{ln:5d} │ {lines[ln - 1]}")


THREAD_FIELDS = [
    "author",
    "body",
    "context",
    "createdAt",
    "id",
    "kind",
    "lastCommentBy",
    "line",
    "lineType",
    "needsReply",
    "openTasks",
    "outdated",
    "path",
    "replies",
    "state",
    "url",
]


def new_pr_threads_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(o.repo)
        pr = resolve_pr(c, r, arg_or(o))
        threads = build_threads(c, r, pr)
        started_by = me_or(c, o.started_by) if o.started_by else ""
        sel = [
            t
            for t in threads
            if not (
                (not o.all and not t.open())
                or (o.needs_reply and not t.needs_reply)
                or (started_by and t.author.lower() != started_by.lower())
            )
        ]
        # Group by file so related items are handled together; general comments last.
        sel.sort(key=lambda t: (t.path == "", t.path, t.line))
        context_lines = o.context
        if exp.enabled() and not exp.wants("context"):
            context_lines = 0
        add_context(c, r, pr, sel, context_lines)
        if exp.enabled():
            exp.write([pick(t.to_json(), exp.fields) for t in sel])
            return
        print_threads(pr, sel)

    cmd = Command(
        "threads [<number> | <url> | <branch>]",
        short="List open review threads and tasks on a pull request",
        long="""List comment threads and tasks on a pull request: the work queue for
addressing feedback, and the follow-up list when re-reviewing.

By default only open items are shown: unresolved threads, open tasks, and
resolved threads that still contain an open task. Each item shows its ID
(for "bb pr comment --reply-to ID [--resolve]"), file:line anchor, the code
the comment was made on, all replies, and whether it needs a reply (the
last comment is not yours).

"outdated" means the commented line no longer exists in the current diff.""",
        example="""  $ bb pr threads 42
  $ bb pr threads 42 --needs-reply
  $ bb pr threads 42 --started-by @me --needs-reply    # replies to my review
  $ bb pr threads 42 --json id,kind,path,line,body,replies,context""",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("all", kind="bool", help="Include resolved threads and tasks")
    cmd.flag("needs-reply", kind="bool", help="Only items whose last comment is not yours")
    cmd.flag("started-by", help='Only threads started by this user ("@me" for yourself)')
    cmd.flag("context", "C", kind="int", default=3, help="Lines of code context around anchored comments (0 to disable)")
    exp = add_json_flags(cmd, THREAD_FIELDS)
    return cmd


def print_threads(pr: dict, items: list[Thread]) -> None:
    reply = 0
    for t in items:
        if t.needs_reply:
            reply += 1
        head = [bold(f"#{t.id}"), t.kind.upper(), t.state]
        if t.path:
            loc = t.path + (f":{t.line} ({t.line_type})" if t.line > 0 else "")
            head.append(cyan(loc))
        else:
            head.append("general")
        if t.outdated:
            head.append(yellow("outdated"))
        head.append("by " + t.author)
        if t.needs_reply:
            head.append(yellow("needs reply"))
        output.out(" · ".join(head))
        for line in t.body.split("\n"):
            output.out("  " + line)
        for line in t.context:
            output.out("    " + gray(line))
        for rp in t.replies:
            tag = f" [task {rp['state']}]" if rp["isTask"] else ""
            lines = rp["body"].split("\n")
            output.out(f"  ↳ #{rp['id']} {rp['author']}{tag}: {lines[0]}")
            for line in lines[1:]:
                output.out("      " + line)
        output.out()
    pid = pr.get("id", 0)
    if not items:
        output.out(f"No matching threads or tasks on #{pid}.")
        return
    output.out(
        f'{len(items)} item(s) on #{pid}, {reply} need a reply. Reply/resolve: bb pr comment {pid} --reply-to <id> -b "..." [--resolve]'
    )

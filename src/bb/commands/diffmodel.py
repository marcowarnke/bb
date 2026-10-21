from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Any

from .. import bitbucket
from ..bitbucket import Client, change_type, diff_old_path, diff_path
from ..cli import Error
from .common import split_list
from .resolve import Repo


class PathFilter:
    """Selects files by glob: patterns with a "/" are anchored at the repo root
    ("src/api" and "src/api/" match that directory), bare names like "*.lock"
    match at any depth; "*" stays within a path segment, "**" crosses."""

    def __init__(self, include: list[str], exclude: list[str]):
        self.include = [glob_regexp(p) for p in include]
        self.exclude = [glob_regexp(p) for p in split_list(exclude)]

    def empty(self) -> bool:
        return not self.include and not self.exclude

    def match(self, *paths: str) -> bool:
        def hit(res: list[re.Pattern]) -> bool:
            return any(p and r.fullmatch(p) for r in res for p in paths)

        if self.include and not hit(self.include):
            return False
        return not hit(self.exclude)

    def diffs(self, ds: list[dict]) -> list[dict]:
        if self.empty():
            return ds
        return [d for d in ds if self.match(diff_path(d), diff_old_path(d))]


def glob_regexp(p: str) -> re.Pattern:
    p = p.removeprefix("./")
    b = []
    if "/" not in p:
        b.append("(.*/)?")  # a bare name like "*.lock" matches at any depth
    i = 0
    while i < len(p):
        c = p[i]
        if c == "*" and i + 1 < len(p) and p[i + 1] == "*":
            b.append(".*")
            i += 1
            if i + 1 < len(p) and p[i + 1] == "/":
                i += 1
        elif c == "*":
            b.append("[^/]*")
        elif c == "?":
            b.append("[^/]")
        else:
            b.append(re.escape(c))
        i += 1
    # A bare name also matches everything below it (directory prefix).
    b.append(".*" if p.endswith("/") else "(/.*)?")
    return re.compile("".join(b), re.DOTALL)


@dataclass
class RawSection:
    """One file's chunk of a unified diff."""

    path: str = ""
    old_path: str = ""
    text: str = ""


DIFF_HEADER = re.compile(r"^diff --git a/(.*) b/(.*)$", re.DOTALL)


def split_after_newline(s: str) -> list[str]:
    """strings.SplitAfter(s, "\n") without the empty tail: only "\n" ends a line."""
    parts = s.split("\n")
    out = [p + "\n" for p in parts[:-1]]
    if parts[-1]:
        out.append(parts[-1])
    return out


def split_raw_diff(d: str) -> list[RawSection]:
    out: list[RawSection] = []
    for chunk in split_after_newline(d):
        m = DIFF_HEADER.match(chunk.rstrip("\n"))
        if m:
            out.append(RawSection(path=m.group(2), old_path=m.group(1)))
        if not out:
            out.append(RawSection())
        out[-1].text += chunk
    return out


def limit_sections(secs: list[RawSection], max_lines: int) -> tuple[str, list[str]]:
    """Emit whole files until max_lines would be exceeded and report the rest;
    the first file is cut rather than dropped."""
    b, omitted, used = [], [], 0
    for s in secs:
        n = s.text.count("\n")
        if max_lines <= 0 or used + n <= max_lines:
            b.append(s.text)
            used += n
        elif used == 0:
            b.append("".join(split_after_newline(s.text)[:max_lines]))
            used = max_lines
            omitted.append(s.path + " (partially shown)")
        else:
            omitted.append(s.path)
    return "".join(b), omitted


NUMBERED_LEGEND = (
    "# Columns: OLD NEW MARK text. Comment with --line NEW for '+' and ' ' lines, --line OLD --line-type removed for '-' lines.\n"
)


def numbered(f: dict) -> str:
    """One file with explicit old/new line numbers."""
    b = [f"=== {diff_path(f)}"]
    ct = change_type(f)
    if ct == "ADD":
        b.append(" (added)")
    elif ct == "DELETE":
        b.append(" (deleted)")
    elif ct == "RENAME":
        b.append(f" (renamed from {diff_old_path(f)})")
    b.append("\n")
    if f.get("binary"):
        b.append("(binary file)\n")
        return "".join(b)
    for h in f.get("hunks") or []:
        b.append(f"@@ -{h.get('sourceLine', 0)},{h.get('sourceSpan', 0)} +{h.get('destinationLine', 0)},{h.get('destinationSpan', 0)} @@\n")
        for s in h.get("segments") or []:
            for line in s.get("lines") or []:
                old_n, new_n, mark = str(line.get("source", 0)), str(line.get("destination", 0)), " "
                if s.get("type") == "ADDED":
                    old_n, mark = "", "+"
                elif s.get("type") == "REMOVED":
                    new_n, mark = "", "-"
                b.append(f"{old_n:>5} {new_n:>5} {mark} {line.get('line', '')}\n")
        if h.get("truncated"):
            b.append("(hunk truncated by server)\n")
    if f.get("truncated"):
        b.append("(file diff truncated by server)\n")
    return "".join(b)


def diff_stat(f: dict) -> tuple[int, int]:
    added = removed = 0
    for h in f.get("hunks") or []:
        for s in h.get("segments") or []:
            if s.get("type") == "ADDED":
                added += len(s.get("lines") or [])
            elif s.get("type") == "REMOVED":
                removed += len(s.get("lines") or [])
    return added, removed


def diff_json(f: dict) -> dict:
    """The agent-facing structured diff."""
    added, removed = diff_stat(f)
    hunks = []
    for h in f.get("hunks") or []:
        lines = []
        for s in h.get("segments") or []:
            typ = s.get("type", "")
            for ln in s.get("lines") or []:
                lines.append(
                    {
                        "type": typ.lower(),
                        "text": ln.get("line", ""),
                        "old": ln.get("source", 0) if typ != "ADDED" else None,
                        "new": ln.get("destination", 0) if typ != "REMOVED" else None,
                    }
                )
        hunks.append(
            {
                "oldStart": h.get("sourceLine", 0),
                "oldLines": h.get("sourceSpan", 0),
                "newStart": h.get("destinationLine", 0),
                "newLines": h.get("destinationSpan", 0),
                "truncated": bool(h.get("truncated")),
                "lines": lines or None,
            }
        )
    m: dict[str, Any] = {
        "path": diff_path(f),
        "changeType": change_type(f),
        "binary": bool(f.get("binary")),
        "truncated": bool(f.get("truncated")),
        "additions": added,
        "deletions": removed,
        "hunks": hunks or None,
    }
    old = diff_old_path(f)
    if old and old != diff_path(f):
        m["oldPath"] = old
    return m


def anchor_for(f: dict | None, path: str, line: int, line_type: str) -> dict:
    """Validate that a comment can be attached to line (new-file number, or
    old-file number for removed lines) and build the Bitbucket anchor;
    line_type "" infers ADDED or CONTEXT from the diff."""
    if f is None:
        raise Error(f"{path} is not changed in this pull request")
    anchor: dict[str, Any] = {"path": diff_path(f), "diffType": "EFFECTIVE"}
    old = diff_old_path(f)
    if old and old != diff_path(f):
        anchor["srcPath"] = old
    if line <= 0:
        return anchor  # file-level comment
    line_type = line_type.upper()
    new_lines: list[int] = []
    old_lines: list[int] = []
    found = ""
    for h in f.get("hunks") or []:
        for s in h.get("segments") or []:
            typ = s.get("type", "")
            for ln in s.get("lines") or []:
                if typ == "REMOVED":
                    old_lines.append(ln.get("source", 0))
                    if line_type == "REMOVED" and ln.get("source", 0) == line:
                        found = typ
                    continue
                new_lines.append(ln.get("destination", 0))
                if ln.get("destination", 0) == line and line_type in ("", typ) and not found:
                    found = typ
    if not found:
        if line_type == "REMOVED":
            msg = f"{path}: old line {line} is not a removed line in this diff"
        else:
            msg = f"{path}: new line {line} is not part of this diff"
            if line_type:
                msg += f" as a {line_type.lower()} line"
        msg += f"\n  commentable new-file lines (--line N): {ranges(new_lines)}"
        if old_lines:
            msg += f"\n  removed old-file lines (--line N --line-type removed): {ranges(old_lines)}"
        msg += f"\n  (see `bb pr diff <pr> -n -- {path}`)"
        raise Error(msg)
    anchor.update(line=line, lineType=found, fileType="FROM" if found == "REMOVED" else "TO")
    return anchor


def ranges(ns: list[int]) -> str:
    """Compress line numbers: [1 2 3 7 9 10] → "1-3, 7, 9-10"."""
    if not ns:
        return "(none)"
    ns = sorted(ns)
    parts = []
    start = prev = ns[0]

    def flush() -> None:
        parts.append(str(start) if start == prev else f"{start}-{prev}")

    for n in ns[1:]:
        if n in (prev, prev + 1):
            prev = n
            continue
        flush()
        start = prev = n
    flush()
    return ", ".join(parts)


@dataclass
class CommentSpec:
    """One comment, from flags or a review file."""

    path: str = ""
    line: int = 0
    lineType: str = ""  # noqa: N815 - the review file's field name
    body: str = ""
    task: bool = False
    suggestion: str | None = None
    replyTo: int = 0  # noqa: N815

    def location(self) -> str:
        if self.replyTo > 0:
            return f"reply:{self.replyTo}"
        if not self.path:
            return "general"
        if self.line > 0:
            return f"{self.path}:{self.line}"
        return self.path


@dataclass
class DiffCache:
    """Per-file PR diffs at the server's default context, which is what
    Bitbucket uses to decide where comments can go."""

    c: Client
    r: Repo
    pr_id: int
    files: dict[str, dict | None] = field(default_factory=dict)

    def file(self, path: str) -> dict | None:
        if path in self.files:
            return self.files[path]
        try:
            d: dict | None = self.c.pr_diff(self.r.project, self.r.slug, self.pr_id, path, -1)
        except bitbucket.APIError as e:
            if e.status != 404:
                raise
            d = None
        f = None
        for x in (d or {}).get("diffs") or []:
            if diff_path(x) == path or diff_old_path(x) == path:
                f = x
                break
        self.files[path] = f
        return f

    def comment_payload(self, spec: CommentSpec) -> dict:
        """Build and validate one comment."""
        body = spec.body.strip()
        if spec.suggestion is not None:
            if not spec.path or spec.line <= 0 or spec.lineType.lower() == "removed":
                raise Error("a suggestion needs --path and --line on a new-file line")
            body += "\n\n```suggestion\n" + spec.suggestion.rstrip("\n") + "\n```"
        if not body.strip():
            raise Error("comment body is empty")
        if spec.line > 0 and not spec.path:
            raise Error(f"line {spec.line} given without a path")
        lt = spec.lineType.lower()
        if lt and lt not in ("added", "removed", "context"):
            raise Error(f'invalid line type "{spec.lineType}": valid values are {{added|removed|context}}')
        payload: dict[str, Any] = {"text": body}
        if spec.task:
            payload["severity"] = "BLOCKER"
        if spec.replyTo > 0:
            payload["parent"] = {"id": spec.replyTo}
            return payload
        if spec.path:
            payload["anchor"] = anchor_for(self.file(spec.path), spec.path, spec.line, spec.lineType)
        return payload

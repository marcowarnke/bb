"""Helpers shared by the commands."""

from __future__ import annotations

import time
from datetime import datetime, timezone

from .. import output
from ..cli import Opts

# sleep is swappable for tests.
sleep = time.sleep


def ts(ms: int | None) -> str:
    """Epoch milliseconds as RFC 3339 (UTC); "" for 0."""
    if not ms:
        return ""
    return datetime.fromtimestamp(ms / 1000, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def ago(ms: int | None) -> str:
    return output.ago_seconds(time.time() - (ms or 0) / 1000)


def first_non_empty(*xs: str) -> str:
    for x in xs:
        if x:
            return x
    return ""


def arg_or(o: Opts) -> str:
    return o.args[0] if o.args else ""


def short(sha: str) -> str:
    return sha[:12]


def read_body(o: Opts, body: str, body_file: str) -> tuple[str, bool]:
    """Resolve --body / --body-file ("-" means stdin): (text, given)."""
    if o.changed("body-file"):
        if body_file == "-":
            data = output.read_stdin()
        else:
            with open(body_file, encoding="utf-8") as f:
                data = f.read()
        return data.rstrip("\n"), True
    return body, o.changed("body")


def split_list(vs: list[str]) -> list[str]:
    """Flatten repeated and comma-separated flag values."""
    out = []
    for v in vs:
        for p in v.split(","):
            p = p.strip()
            if p:
                out.append(p)
    return out


def prompt(label: str) -> str:
    output.io.stderr.write(label)
    output.io.stderr.flush()
    return output.io.stdin.readline().strip()

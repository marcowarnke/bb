"""The review and apply-feedback loops. Fixtures: see x_diff/docs_diff and the activities in conftest.py."""

from __future__ import annotations

import pytest
from conftest import jq, run
from fakes import PR_BASE

from bb.commands.diffmodel import NUMBERED_LEGEND, glob_regexp

pytestmark = pytest.mark.jq


def test_diff_line_numbers_and_filters(fake):
    out, err, code = run("pr", "diff", "7", "-R", "PROJ/repo", "-n", "--", "src/")
    assert code == 0, err
    for want in [
        NUMBERED_LEGEND,
        "=== src/x.go\n",
        "    1     1   CONTEXT 1/1",
        "    2       - REMOVED 2/2",
        "          3 + ADDED 3/3",
        "    4     5   CONTEXT 4/5",
    ]:
        assert want in out
    assert "docs/a.md" not in out, "path filter did not exclude docs/a.md"
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "-n", "--exclude", "src/**")
    assert "src/x.go" not in out and "=== docs/a.md (added)" in out
    # Raw diff mode filters too.
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "--", "nothing/")
    assert out == ""


def test_diff_stat_json_and_max_lines(fake):
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "--stat")
    assert "    +2     -1  src/x.go" in out and "2 files changed, +4 -1" in out
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "--json")
    assert jq(out, '.files[0].hunks[0].lines[1] | "\\(.type) \\(.old) \\(.new)"') == "removed 2 null"
    out, err, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "-n", "--max-lines", "6")
    assert out.count("\n") <= 7 and "src/x.go (partially shown)" in err and "docs/a.md" in err


def test_diff_since_last_review(fake):
    out, err, code = run("pr", "diff", "7", "-R", "PROJ/repo", "--since-last-review", "-n")
    assert code == 0 and "Changes from old111 to abc123" in err and "=== src/x.go" in out and "docs/a.md" not in out
    _, err, code = run("pr", "diff", "7", "-R", "PROJ/repo", "--since", "gone")
    assert code != 0 and "rebased or force-pushed" in err
    _, err, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "--since", "abc123")
    assert "No changes since" in err


def test_comment_line_validation(fake):
    def anchor_of(*args):
        _, err, code = run("pr", "comment", "7", "-R", "PROJ/repo", "-b", "x", *args)
        if code != 0:
            return None, err
        return fake.bodies[f"POST {PR_BASE}/7/comments"]["anchor"], ""

    a, _ = anchor_of("--path", "src/x.go", "--line", "3")
    assert a["lineType"] == "ADDED" and a["fileType"] == "TO"
    a, _ = anchor_of("--path", "src/x.go", "--line", "4")
    assert a["lineType"] == "CONTEXT"
    _, err = anchor_of("--path", "src/x.go", "--line", "9")
    assert (
        "new line 9 is not part of this diff" in err
        and "commentable new-file lines (--line N): 1-5" in err
        and "removed old-file lines (--line N --line-type removed): 2" in err
    )
    _, err = anchor_of("--path", "src/x.go", "--line", "3", "--line-type", "removed")
    assert "old line 3 is not a removed line" in err
    _, err = anchor_of("--path", "nope.go", "--line", "1")
    assert "nope.go is not changed in this pull request" in err
    a, _ = anchor_of("--path", "docs/a.md")
    assert a["path"] == "docs/a.md" and "line" not in a
    _, err, code = run(
        "pr", "comment", "7", "-R", "PROJ/repo", "-b", "use this", "--path", "src/x.go", "--line", "3", "--suggestion", "\tbetter()"
    )
    body = fake.bodies[f"POST {PR_BASE}/7/comments"]
    assert code == 0 and body["text"] == "use this\n\n```suggestion\n\tbetter()\n```", err


def test_review_input(fake):
    bad = """{"event":"REQUEST_CHANGES","body":"Summary","comments":[
        {"path":"src/x.go","line":3,"body":"ok"},
        {"path":"src/x.go","line":42,"body":"bad line"},
        {"path":"nope.go","line":1,"body":"bad file"}]}"""
    _, err, code = run("pr", "review", "7", "-R", "PROJ/repo", "--input", "-", stdin=bad)
    assert code != 0 and "2 of 3 comments are invalid" in err and "comments[1] (src/x.go:42)" in err and "comments[2] (nope.go:1)" in err
    assert not fake.all.get(f"POST {PR_BASE}/7/comments") and f"PUT {PR_BASE}/7/participants/me" not in fake.bodies

    good = """{"event":"REQUEST_CHANGES","body":"Summary","comments":[
        {"path":"src/x.go","line":3,"body":"must fix","task":true},
        {"path":"src/x.go","line":2,"lineType":"removed","body":"why removed?"},
        {"body":"general note"}]}"""
    out, err, code = run("pr", "review", "7", "-R", "PROJ/repo", "--input", "-", "--dry-run", stdin=good)
    assert (
        code == 0 and out == "src/x.go:3\ttask\nsrc/x.go:2\tcomment\ngeneral\tcomment\n" and not fake.all.get(f"POST {PR_BASE}/7/comments")
    ), err

    out, err, code = run("pr", "review", "7", "-R", "PROJ/repo", "--input", "-", stdin=good)
    assert code == 0, err
    posts = fake.all[f"POST {PR_BASE}/7/comments"]
    assert len(posts) == 4 and posts[0]["severity"] == "BLOCKER" and posts[1]["anchor"]["fileType"] == "FROM"
    assert "anchor" not in posts[2] and posts[3]["text"] == "Summary"
    assert fake.bodies[f"PUT {PR_BASE}/7/participants/me"]["status"] == "NEEDS_WORK"
    lines = out.strip().split("\n")
    assert len(lines) == 4 and lines[0].startswith("src/x.go:3\thttp") and lines[3].startswith("summary\thttp")
    assert "Requested changes to pull request PROJ/repo#7 at abc123 (3 comments, 1 tasks)" in err

    # Flags override the file's event; unknown fields are rejected.
    run("pr", "review", "7", "-R", "PROJ/repo", "--input", "-", "--approve", stdin='{"body":"lgtm"}')
    assert fake.bodies[f"PUT {PR_BASE}/7/participants/me"]["status"] == "APPROVED"
    _, err, code = run("pr", "review", "7", "-R", "PROJ/repo", "--input", "-", stdin='{"event":"APPROVE","coments":[]}')
    assert code != 0 and 'unknown field "coments"' in err


def test_threads(fake):
    def ids(*args):
        out, err, code = run("pr", "threads", "7", "-R", "PROJ/repo", "--json", "id", "--jq", '[.[].id] | map(tostring) | join(",")', *args)
        assert code == 0, err
        return out.strip()

    # Sorted by file/line: task #12 at src/x.go:2, thread #10 at src/x.go:5; resolved #13 hidden.
    assert ids() == "12,10"
    assert ids("--all") == "12,10,13"
    assert ids("--needs-reply") == "10"
    assert ids("--started-by", "@me") == "12"

    out, _, _ = run("pr", "threads", "7", "-R", "PROJ/repo", "--json", "id,kind,lastCommentBy,needsReply,context,replies")
    assert jq(out, '.[1] | "\\(.kind) \\(.lastCommentBy) \\(.needsReply) \\(.replies[0].id)"') == "thread alice true 11"
    assert (
        jq(out, '"[" + (.[0].context | join("|")) + "]"')
        == "[     1 │ package x|>    2 │ new()|     3 │ extra()|     4 │ tail|     5 │ end]"
    )

    out, _, _ = run("pr", "threads", "7", "-R", "PROJ/repo")
    for want in [
        "#12 · TASK · open · src/x.go:2 (added) · by me",
        "#10 · THREAD · open · src/x.go:5 (added) · by bob · needs reply",
        "  ↳ #11 alice: reply",
        ">    5 │ end",
        "2 item(s) on #7, 1 need a reply",
    ]:
        assert want in out


def test_reply_and_resolve(fake):
    # Replying to #11 resolves its thread root #10.
    out, err, code = run("pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "11", "-b", "Fixed in abc123", "--resolve")
    assert code == 0 and "commentId=" in out and "Resolved thread 10" in err, err
    assert fake.bodies[f"POST {PR_BASE}/7/comments"]["parent"]["id"] == 11
    p = fake.bodies[f"PUT {PR_BASE}/7/comments/10"]
    assert p["threadResolved"] is True and p["version"] == 4
    # Tasks resolve by state; the body is optional.
    posts = len(fake.all[f"POST {PR_BASE}/7/comments"])
    _, err, code = run("pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "12", "--resolve")
    assert code == 0 and fake.bodies[f"PUT {PR_BASE}/7/comments/12"]["state"] == "RESOLVED", err
    assert len(fake.all[f"POST {PR_BASE}/7/comments"]) == posts
    run("pr", "comment", "7", "-R", "PROJ/repo", "--reply-to", "12", "--reopen")
    assert fake.bodies[f"PUT {PR_BASE}/7/comments/12"]["state"] == "OPEN"
    _, err, code = run("pr", "comment", "7", "-R", "PROJ/repo", "--resolve")
    assert code != 0 and "--reply-to" in err


def test_checks_watch_and_view_fields(fake):
    out, err, code = run("pr", "checks", "7", "-R", "PROJ/repo", "--watch")
    assert code == 0 and "waiting: 1 pending (Lint)" in err and "Lint\tpass" in out, (out, err)
    out, _, _ = run("pr", "view", "7", "-R", "PROJ/repo", "--json", "commits,unresolvedThreads")
    assert jq(out, '"\\(.unresolvedThreads) \\(.commits[0].messageHeadline)"') == "2 Add feature"


@pytest.mark.parametrize(
    "pattern,match,no_match",
    [
        ("src/", ["src/a.go", "src/x/y.go"], ["srcx/a.go", "lib/src/a.go"]),
        ("src/api", ["src/api/a.go", "src/api"], ["src/apiv2/a.go"]),
        ("*.lock", ["go.lock", "web/yarn.lock"], ["lock.go"]),
        ("vendor/**", ["vendor/a/b.go"], ["src/vendor/a.go"]),
        ("**/*_test.go", ["a_test.go", "pkg/a_test.go"], ["pkg/a.go"]),
        ("cmd/*.go", ["cmd/main.go"], ["cmd/sub/main.go"]),
    ],
)
def test_path_filter(pattern, match, no_match):
    r = glob_regexp(pattern)
    for p in match:
        assert r.fullmatch(p), (pattern, p)
    for p in no_match:
        assert not r.fullmatch(p), (pattern, p)

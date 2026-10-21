from __future__ import annotations

import json

import pytest
from conftest import run
from fakes import PR_BASE

pytestmark = pytest.mark.jq


def test_pr_list_and_json(fake):
    out, err, code = run("pr", "list", "-R", "PROJ/repo")
    assert code == 0 and out.startswith("7\tAdd feature\tfeat\tOPEN\t"), err
    out, _, _ = run("pr", "list", "-R", "PROJ/repo", "--json", "number,reviewDecision", "--jq", ".[0].reviewDecision")
    assert out.strip() == "APPROVED"
    _, err, code = run("pr", "list", "-R", "PROJ/repo", "--json", "nope")
    assert code != 0 and "Unknown JSON field" in err
    out, _, _ = run("pr", "list", "-R", "PROJ/repo", "--json", "number,title,headRefName", "--format", "#{number} {title} [{headRefName}]")
    assert out == "#7 Add feature [feat]\n"


def test_pr_view_comments(fake):
    out, err, code = run("pr", "view", "7", "-R", "PROJ/repo", "--comments")
    assert code == 0, err
    for want in [
        "title:\tAdd feature",
        "reviewers:\tbob (Approved), carol (Requested)",
        "id:10",
        "src/x.go:5 (ADDED)",
        "  ↳ alice",
        "reply",
    ]:
        assert want in out
    # The reply must only appear nested, not as its own root.
    assert out.count("id:11") == 1
    out, _, _ = run(
        "pr", "view", "7", "-R", "PROJ/repo", "--json", "comments,files", "--jq", ".comments[0].replies[0].body, .files[0].path"
    )
    assert out == "reply\nsrc/x.go\n"


def test_pr_view_by_url(fake):
    out, err, code = run(
        "pr",
        "view",
        fake.url + "/projects/proj/repos/repo/pull-requests/7/overview",
        "-R",
        "OTHER/x",
        "--json",
        "number",
        "--jq",
        ".number",
    )
    assert code == 0 and out.strip() == "7", err


def test_pr_diff(fake):
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo")
    assert out == "diff --git a/x b/x\n+added\n"
    out, _, _ = run("pr", "diff", "7", "-R", "PROJ/repo", "--name-only")
    assert out == "src/x.go\n"


def test_pr_inline_comment(fake):
    out, err, code = run(
        "pr", "comment", "7", "-R", "PROJ/repo", "-b", "nit", "--path", "src/x.go", "--line", "2", "--line-type", "removed", "--task"
    )
    assert code == 0 and "/pull-requests/7/overview?commentId=" in out, err
    body = fake.bodies[f"POST {PR_BASE}/7/comments"]
    a = body["anchor"]
    assert (a["fileType"], a["lineType"], a["line"], body["severity"]) == ("FROM", "REMOVED", 2, "BLOCKER")
    run("pr", "comment", "7", "-R", "PROJ/repo", "-F", "-", "--reply-to", "10", stdin="body from stdin")
    body = fake.bodies[f"POST {PR_BASE}/7/comments"]
    assert body["text"] == "body from stdin" and body["parent"]["id"] == 10


def test_pr_review(fake):
    _, err, code = run("pr", "review", "7", "-R", "PROJ/repo", "--request-changes", "-b", "please fix")
    assert code == 0, err
    assert fake.bodies[f"PUT {PR_BASE}/7/participants/me"]["status"] == "NEEDS_WORK"
    assert fake.bodies[f"POST {PR_BASE}/7/comments"]["text"] == "please fix"
    assert run("pr", "review", "7", "-R", "PROJ/repo", "--approve", "--comment")[2] != 0


def test_pr_create(fake):
    out, err, code = run("pr", "create", "-R", "PROJ/repo", "--head", "feat2", "-t", "T", "-b", "B", "-r", "bob,dave", "--draft")
    assert code == 0, err
    assert "/pull-requests/7" in out
    body = fake.bodies[f"POST {PR_BASE}"]
    # bob (direct), dave.smith (fuzzy match), carol (default reviewer); "me" excluded as author.
    assert [r["user"]["name"] for r in body["reviewers"]] == ["bob", "dave.smith", "carol"]
    assert body["toRef"]["id"] == "refs/heads/main" and body["draft"] is True
    _, err, code = run("pr", "create", "-R", "PROJ/repo", "--head", "missing", "-t", "T")
    assert code != 0 and "push it first" in err
    _, err, code = run("pr", "create", "-R", "PROJ/repo", "--head", "feat2")
    assert code != 0 and "--title" in err
    out, _, code = run("pr", "create", "-R", "PROJ/repo", "--head", "feat2", "-t", "T", "--dry-run", "--no-default-reviewers")
    assert code == 0 and json.loads(out)["title"] == "T"


def test_pr_edit_reviewers(fake):
    _, err, code = run("pr", "edit", "7", "-R", "PROJ/repo", "--add-reviewer", "dave", "--remove-reviewer", "bob")
    assert code == 0, err
    body = fake.bodies[f"PUT {PR_BASE}/7"]
    assert body["reviewers"] == [{"user": {"name": "carol"}}, {"user": {"name": "dave.smith"}}] and body["version"] == 3


def test_pr_merge_and_checks(fake):
    _, err, code = run("pr", "merge", "7", "-R", "PROJ/repo", "--squash")
    assert code == 0 and fake.bodies[f"POST {PR_BASE}/7/merge"]["strategyId"] == "squash", err
    out, _, code = run("pr", "checks", "7", "-R", "PROJ/repo")
    assert code == 8 and "CI\tpass\thttp://ci" in out and "Lint\tpending" in out


def test_api_command(fake):
    out, err, code = run("api", "projects/{project}/repos/{repo}/pull-requests", "-R", "PROJ/repo", "--paginate", "--jq", ".[].title")
    assert code == 0 and out == "Add feature\n", err
    run("api", "-R", "PROJ/repo", "projects/{project}/repos/{repo}/pull-requests/7/comments", "-f", "text=hi", "-F", "parent[id]=10")
    body = fake.bodies[f"POST {PR_BASE}/7/comments"]
    assert body["text"] == "hi" and body["parent"]["id"] == 10


def test_auth_failure_exit_code(fake, monkeypatch):
    monkeypatch.setenv("BB_TOKEN", "wrong")
    _, err, code = run("pr", "view", "7", "-R", "PROJ/repo")
    assert "HTTP 401" in err and code == 4


def test_all_commands_help():
    """Every command must build its flag set and print help (e.g. -h clashes)."""
    from bb.commands.root import new_root

    n = 0

    def walk(c):
        nonlocal n
        for sub in c.commands:
            path = sub.path().split()[1:]
            out, err, code = run(*path, "--help")
            assert code == 0 and "Usage:" in out, (path, err)
            n += 1
            walk(sub)

    walk(new_root())
    assert n >= 20


def test_merge_blockers(fake):
    out, _, _ = run("pr", "view", "7", "-R", "PROJ/repo")
    assert "merge:\tready\n" in out
    fake.merge = {
        "canMerge": False,
        "conflicted": True,
        "vetoes": [
            {"summaryMessage": "Not all required reviewers have approved yet.", "detailedMessage": "At least 2 approvals are required."},
            {"summaryMessage": "Open tasks", "detailedMessage": "Open tasks"},
        ],
    }
    out, _, _ = run("pr", "view", "7", "-R", "PROJ/repo")
    assert (
        "merge:\tblocked: the pull request has merge conflicts; Not all required reviewers have approved yet: "
        "At least 2 approvals are required.; Open tasks\n"
    ) in out
    out, _, _ = run(
        "pr",
        "view",
        "7",
        "-R",
        "PROJ/repo",
        "--json",
        "mergeStateStatus,mergeBlockers",
        "--jq",
        ".mergeStateStatus, (.mergeBlockers | length)",
    )
    assert out == "DIRTY\n3\n"
    _, err, code = run("pr", "merge", "7", "-R", "PROJ/repo", "--dry-run")
    assert code == 1 and "  - Open tasks" in err
    fake.merge = None
    _, err, code = run("pr", "merge", "7", "-R", "PROJ/repo", "--dry-run", "--squash")
    assert code == 0 and "can be merged (squash)" in err and f"POST {PR_BASE}/7/merge" not in fake.bodies


@pytest.mark.parametrize(
    "args,want",
    [
        ([], "/browse"),
        (["42"], "/pull-requests/42"),
        (["src/main.go:42", "-b", "release/1.0"], "/browse/src/main.go?at=refs%2Fheads%2Frelease%2F1.0#42"),
        (["./docs/a b.md:3-7"], "/browse/docs/a%20b.md#3-7"),
        (["abc1234"], "/commits/abc1234"),
        (["--commit=abc1234", "src"], "/browse/src?at=abc1234"),
        (["-s"], "/settings"),
    ],
)
def test_browse(fake, args, want):
    out, err, code = run("browse", "-R", "PROJ/repo", "-n", *args)
    assert code == 0 and out.strip() == fake.url + "/projects/PROJ/repos/repo" + want, err


def test_browse_personal_and_errors(fake):
    out, _, _ = run("browse", "-R", "~Alice/repo", "-n")
    assert out.strip() == fake.url + "/users/alice/repos/repo/browse"
    _, err, code = run("browse", "-R", "PROJ/repo", "-n", "x.go:abc")
    assert code != 0 and "invalid line" in err


def test_status_dashboard(fake):
    fake.merge = {"canMerge": False, "vetoes": [{"summaryMessage": "Needs 2 approvals"}]}
    out, err, code = run("status")
    want = (
        f"authored\tPROJ/repo\t7\tAdd feature\t{fake.url}/projects/PROJ/repos/repo/pull-requests/7\tblocked: Needs 2 approvals\n"
        "reviewRequested\tOTHER/lib\t9\tFix bug\t"
    )
    assert code == 0 and out.startswith(want) and "\tby alice, updated " in out, err
    out, _, _ = run(
        "status", "--json", "number,repository", "--jq", ".reviewRequested[0].repository.nameWithOwner, (.waitingOnAuthor | length)"
    )
    assert out == "OTHER/lib\n0\n"


@pytest.mark.parametrize(
    "args,want",
    [
        (["pr", "view", "-R", "PROJ/repo", ""], "7\tAdd feature · alice · feat\n:36"),
        (["pr", "merge", "-R", "PROJ/repo", "#"], "7\tAdd feature · alice · feat\n:36"),
        (["pr", "view", "-R", "PROJ/repo", "8"], ":36"),  # no match
        (["pr", "view", "-R", "PROJ/repo", "7", ""], ":4"),  # one PR only
        (["browse", "-R", "PROJ/repo", "src/"], ":0"),  # files
        (["pr", "create", "-R", "PROJ/repo", "-r", "bob,da"], "bob,dave.smith\tDave Smith\n:4"),
        (["pr", "list", "-R", "PROJ/repo", "--author", "@"], "@me\tyourself\ndave.smith\tDave Smith\n:4"),
        (["pr", "create", "-R", "PROJ/repo", "-B", "f"], "feat2\n:36"),
        (["pr", "list", "-R", "pr"], "prOJ/\tProject\n:6"),
        (["pr", "list", "-R", "proj/"], "proj/repo\tRepo\nproj/lib\tLib\n:4"),
        (["pr", "list", "--state", ""], "open\nclosed\nmerged\nall\n:4"),
    ],
)
def test_completion(fake, args, want):
    out, err, code = run("__complete", *args)
    assert code == 0, err
    assert out.strip() == want

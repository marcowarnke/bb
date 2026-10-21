"""In-process fake Bitbucket Data Center, shared by the tests and scripts/verify_artifact.py."""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlsplit

PR_BASE = "/rest/api/1.0/projects/PROJ/repos/repo/pull-requests"

# Fixture diff for src/x.go (old → new):
#
#   old 1 package x   new 1 package x
#   old 2 old()       (removed)
#                     new 2 new()    (added)
#                     new 3 extra()  (added)
#   old 3 tail        new 4 tail
#   old 4 end         new 5 end
X_CONTENT = "package x\nnew()\nextra()\ntail\nend\n"


def x_diff() -> dict:
    def seg(typ: str, *lines: tuple[int, int]) -> dict:
        return {"type": typ, "lines": [{"source": s, "destination": d, "line": f"{typ} {s}/{d}"} for s, d in lines]}

    return {
        "source": {"toString": "src/x.go"},
        "destination": {"toString": "src/x.go"},
        "hunks": [
            {
                "sourceLine": 1,
                "sourceSpan": 4,
                "destinationLine": 1,
                "destinationSpan": 5,
                "segments": [seg("CONTEXT", (1, 1)), seg("REMOVED", (2, 2)), seg("ADDED", (3, 2), (3, 3)), seg("CONTEXT", (3, 4), (4, 5))],
            }
        ],
    }


def docs_diff() -> dict:
    return {
        "destination": {"toString": "docs/a.md"},
        "hunks": [
            {
                "sourceLine": 0,
                "sourceSpan": 0,
                "destinationLine": 1,
                "destinationSpan": 2,
                "segments": [
                    {
                        "type": "ADDED",
                        "lines": [{"source": 0, "destination": 1, "line": "# Doc"}, {"source": 0, "destination": 2, "line": "text"}],
                    }
                ],
            }
        ],
    }


def pr_fixture(url: str) -> dict:
    repo = {"id": 1, "slug": "repo", "project": {"key": "PROJ"}}
    return {
        "id": 7,
        "version": 3,
        "title": "Add feature",
        "description": "Body text",
        "state": "OPEN",
        "open": True,
        "createdDate": 1700000000000,
        "updatedDate": 1700000100000,
        "fromRef": {"id": "refs/heads/feat", "displayId": "feat", "latestCommit": "abc123", "repository": repo},
        "toRef": {"id": "refs/heads/main", "displayId": "main", "latestCommit": "def456", "repository": repo},
        "author": {"user": {"name": "alice", "slug": "alice"}},
        "reviewers": [
            {"user": {"name": "bob", "slug": "bob"}, "status": "APPROVED"},
            {"user": {"name": "carol", "slug": "carol"}, "status": "UNAPPROVED"},
        ],
        "participants": [{"user": {"name": "me", "slug": "me"}, "role": "PARTICIPANT", "lastReviewedCommit": "old111"}],
        "links": {"self": [{"href": url + "/projects/PROJ/repos/repo/pull-requests/7"}]},
        "properties": {"commentCount": 2, "openTaskCount": 1},
    }


def page(*vals: Any) -> dict:
    return {"values": list(vals), "isLastPage": True}


class FakeBB:
    """A tiny in-memory Bitbucket Data Center."""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.requests: list[str] = []
        self.bodies: dict[str, dict] = {}  # last body per "METHOD path"
        self.all: dict[str, list[dict]] = {}  # every body per "METHOD path"
        self.next_id = 100
        self.merge: dict | None = None  # GET …/merge response; None = mergeable
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a: Any) -> None:
                pass

            def handle_any(self) -> None:
                fake.handle(self)

            do_GET = do_POST = do_PUT = do_DELETE = handle_any

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def handle(self, h: BaseHTTPRequestHandler) -> None:
        def send(status: int, body: bytes | str = b"", ctype: str = "application/json") -> None:
            if isinstance(body, str):
                body = body.encode()
            h.send_response(status)
            h.send_header("Content-Type", ctype)
            h.send_header("Content-Length", str(len(body)))
            h.end_headers()
            h.wfile.write(body)

        if h.headers.get("Authorization") != "Bearer secret":
            send(401, '{"errors":[{"message":"Authentication failed"}]}')
            return
        u = urlsplit(h.path)
        key = f"{h.command} {u.path}"
        data = h.rfile.read(int(h.headers.get("Content-Length") or 0))
        with self.lock:
            self.requests.append(key + "?" + u.query)
            try:
                m = json.loads(data)
                if isinstance(m, dict):
                    self.bodies[key] = m
                    self.all.setdefault(key, []).append(m)
            except ValueError:
                pass
            self.next_id += 1
            new_id = self.next_id
            build_polls = "\n".join(self.requests).count("build-status")
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        pr = pr_fixture(self.url)
        out: Any
        if key == "GET /plugins/servlet/applinks/whoami":
            send(200, "me", "text/plain")
            return
        elif key == "GET /rest/api/1.0/users/me":
            out = {"name": "me", "slug": "me", "displayName": "Me"}
        elif key == "GET /rest/api/1.0/users/bob":
            out = {"name": "bob", "slug": "bob"}
        elif key == "GET /rest/api/1.0/users/dave":
            send(404)
            return
        elif key == "GET /rest/api/1.0/users":
            out = page({"name": "dave.smith", "slug": "dave.smith", "displayName": "Dave Smith"})
        elif key == f"GET {PR_BASE}/7":
            out = pr
        elif key == "GET /rest/api/1.0/dashboard/pull-requests":
            sel = q.get("role", "") + "/" + q.get("participantStatus", "")
            if sel == "AUTHOR/":
                out = page(pr)
            elif sel == "REVIEWER/UNAPPROVED":
                other = pr_fixture(self.url)
                other["id"], other["title"] = 9, "Fix bug"
                other["toRef"]["repository"] = {"slug": "lib", "project": {"key": "OTHER"}}
                out = page(other)
            else:
                out = page()
        elif key == f"GET {PR_BASE}":
            out = page(pr)
        elif key == f"GET {PR_BASE}/7.diff":
            send(200, "diff --git a/x b/x\n+added\n", "text/plain")
            return
        elif key == f"GET {PR_BASE}/7/changes":
            out = page({"type": "MODIFY", "path": {"toString": "src/x.go"}})
        elif key == f"GET {PR_BASE}/7/activities":
            # Threads: #10 by bob on src/x.go:5 with alice's reply #11 (needs reply from me);
            # task #12 by me on src/x.go:2, open; #13 by carol, resolved.
            reply = {"id": 11, "text": "reply", "author": {"slug": "alice"}, "createdDate": 3}
            root_c = {"id": 10, "text": "root comment", "author": {"slug": "bob"}, "createdDate": 1, "comments": [reply]}
            task = {"id": 12, "text": "handle nil", "author": {"slug": "me"}, "createdDate": 4, "severity": "BLOCKER", "state": "OPEN"}
            done = {"id": 13, "text": "nice", "author": {"slug": "carol"}, "createdDate": 5, "threadResolved": True}
            out = page(
                {"id": 5, "action": "COMMENTED", "comment": done},
                {
                    "id": 4,
                    "action": "COMMENTED",
                    "comment": task,
                    "commentAnchor": {"path": "src/x.go", "line": 2, "lineType": "ADDED", "fileType": "TO", "toHash": "abc123"},
                },
                {"id": 2, "action": "COMMENTED", "comment": reply},
                {
                    "id": 1,
                    "action": "COMMENTED",
                    "comment": root_c,
                    "commentAnchor": {"path": "src/x.go", "line": 5, "lineType": "ADDED", "fileType": "TO", "toHash": "abc123"},
                },
                {"id": 0, "action": "OPENED"},
            )
        elif key == f"POST {PR_BASE}/7/comments":
            out = {"id": new_id}
        elif key in (f"GET {PR_BASE}/7/comments/10", f"GET {PR_BASE}/7/comments/12"):
            out = {"id": 0, "version": 4}
        elif key in (f"PUT {PR_BASE}/7/comments/10", f"PUT {PR_BASE}/7/comments/12"):
            out = {}
        elif key == f"GET {PR_BASE}/7/diff":
            out = {"fromHash": "def456", "toHash": "abc123", "diffs": [x_diff(), docs_diff()]}
        elif key == f"GET {PR_BASE}/7/diff/src/x.go":
            out = {"diffs": [x_diff()]}
        elif key == f"GET {PR_BASE}/7/diff/docs/a.md":
            out = {"diffs": [docs_diff()]}
        elif key == "GET /rest/api/1.0/projects/PROJ/repos/repo/compare/diff":
            if q.get("from") != "abc123" or q.get("to") != "old111":
                send(404)
                return
            out = {"diffs": [x_diff()]}
        elif key == "GET /rest/api/1.0/projects/PROJ/repos/repo/raw/src/x.go":
            if q.get("at") != "abc123":
                send(404)
                return
            send(200, X_CONTENT, "text/plain")
            return
        elif key == f"GET {PR_BASE}/7/commits":
            out = page({"id": "abc123", "message": "Add feature\n\nDetails", "author": {"name": "Alice"}})
        elif key == f"PUT {PR_BASE}/7/participants/me":
            out = {}
        elif key == f"GET {PR_BASE}/7/merge":
            out = self.merge if self.merge is not None else {"canMerge": True}
        elif key in (f"POST {PR_BASE}/7/merge", f"POST {PR_BASE}/7/decline", f"PUT {PR_BASE}/7"):
            out = pr
        elif key == "GET /rest/api/1.0/projects/PROJ/repos/repo/default-branch":
            out = {"displayId": "main"}
        elif key == "GET /rest/api/1.0/projects":
            out = page({"key": "PROJ", "name": "Project"}, {"key": "OTHER", "name": "Other"})
        elif key == "GET /rest/api/1.0/projects/PROJ/repos":
            out = page({"slug": "repo", "name": "Repo"}, {"slug": "lib", "name": "Lib"})
        elif key == "GET /rest/api/1.0/projects/PROJ/repos/repo/branches":
            out = page({"displayId": "feat2"})
        elif key == "GET /rest/api/1.0/projects/PROJ/repos/repo":
            out = {"id": 1, "slug": "repo", "project": {"key": "PROJ"}}
        elif key == "GET /rest/default-reviewers/1.0/projects/PROJ/repos/repo/reviewers":
            out = [{"name": "carol"}, {"name": "me"}]
        elif key == f"POST {PR_BASE}":
            out = pr
        elif key == "GET /rest/build-status/1.0/commits/abc123":
            lint = "SUCCESSFUL" if build_polls > 1 else "INPROGRESS"  # first poll pending, then green
            out = page({"state": "SUCCESSFUL", "name": "CI", "url": "http://ci"}, {"state": lint, "name": "Lint"})
        else:
            send(404, json.dumps({"errors": [{"message": f"no route {key}"}]}))
            return
        send(200, json.dumps(out) + "\n")

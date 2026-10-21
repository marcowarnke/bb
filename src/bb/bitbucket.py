"""A minimal Bitbucket Data Center REST client.

API objects are kept as the dicts the server sends; the helpers below read
them with Go's zero values for missing fields."""

from __future__ import annotations

import http.client
import json
import ssl
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import HTTPSHandler, Request, build_opener

from .cli import Error
from .output import Struct

STATUS_TEXT = {s.value: s.phrase for s in http.HTTPStatus}


def ssl_context() -> ssl.SSLContext:
    """TLS trusting the OS certificate store (corporate CAs included) via
    truststore when available; SSL_CERT_FILE still wins when set."""
    import os

    if not os.environ.get("SSL_CERT_FILE") and not os.environ.get("SSL_CERT_DIR"):
        try:
            import truststore

            return truststore.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        except ImportError:
            pass
    return ssl.create_default_context()


def path_escape(s: str) -> str:
    """Go's url.PathEscape."""
    return quote(s, safe="$&+:=@")


def encode_query(query: dict[str, Any]) -> str:
    """Go's url.Values.Encode: sorted by key."""
    items = []
    for k in sorted(query):
        v = query[k]
        for x in v if isinstance(v, list) else [v]:
            items.append((k, str(x)))
    return urlencode(items)


class APIError(Error):
    def __init__(self, status: int, method: str, url: str, messages: list[str] | None = None):
        self.status, self.method, self.url, self.messages = status, method, url, messages or []
        super().__init__(str(self))

    def __str__(self) -> str:
        msg = "; ".join(self.messages) or STATUS_TEXT.get(self.status, "")
        return f"HTTP {self.status}: {msg} ({self.method} {self.url})"


def is_not_found(e: BaseException | None) -> bool:
    return isinstance(e, APIError) and e.status == 404


@dataclass
class Response:
    status: int
    reason: str
    headers: list[tuple[str, str]]
    body: bytes
    version: str = "HTTP/1.1"

    def header(self, name: str) -> str:
        for k, v in self.headers:
            if k.lower() == name.lower():
                return v
        return ""

    def text(self) -> str:
        return self.body.decode("utf-8", "replace")


class Client:
    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = 60.0
        # The authenticated user's slug, cached.
        self.user_slug = ""
        self._opener = build_opener(HTTPSHandler(context=ssl_context()))

    def url(self, path: str, query: dict[str, Any] | None = None) -> str:
        u = self.base_url + "/" + path.lstrip("/")
        if query:
            u += ("&" if "?" in u else "?") + encode_query(query)
        return u

    def raw(
        self,
        method: str,
        path: str,
        query: dict[str, Any] | None = None,
        headers: list[tuple[str, str]] | None = None,
        body: bytes | None = None,
    ) -> Response:
        """Perform a request; non-2xx statuses raise APIError."""
        u = self.url(path, query)
        req = Request(u, data=body, method=method)
        for k, v in headers or []:
            req.add_header(k, v)
        if not req.has_header("Accept"):
            req.add_header("Accept", "application/json")
        if body is not None and not req.has_header("Content-type"):
            req.add_header("Content-Type", "application/json")
        # Required by Bitbucket's XSRF protection for non-GET API calls.
        req.add_header("X-Atlassian-Token", "no-check")
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with self._opener.open(req, timeout=self.timeout) as r:
                resp = Response(r.status, r.reason, list(r.headers.items()), r.read(), _version(r))
        except HTTPError as e:
            try:
                data = e.read()
            except Exception:
                data = b""
            resp = Response(e.code, e.reason or "", list(e.headers.items()) if e.headers else [], data)
        except URLError as e:
            raise Error(f'{method.title()} "{u}": {e.reason}') from e
        except (OSError, http.client.HTTPException) as e:
            raise Error(f'{method.title()} "{u}": {e}') from e
        if resp.status >= 300:
            raise parse_error(resp, method, u)
        return resp

    def do(self, method: str, path: str, query: dict[str, Any] | None = None, body: Any = None) -> Any:
        """Send body (JSON-encoded if not None) and decode the JSON response."""
        data = json.dumps(body).encode("utf-8") if body is not None else None
        resp = self.raw(method, path, query, None, data)
        if resp.status == 204 or not resp.body.strip():
            return None
        try:
            return json.loads(resp.body)
        except ValueError as e:
            raise Error(f"decoding the response of {method} {self.url(path, query)}: {e}") from None

    def paginate(
        self, path: str, query: dict[str, Any] | None = None, limit: int = 0, keep: Callable[[dict], bool] | None = None, raw: bool = False
    ) -> list:
        """Collect up to limit items (limit <= 0: all) accepted by keep (None: all)."""
        q = dict(query or {})
        page_size = 100
        if 0 < limit < page_size and keep is None:
            page_size = limit
        q["limit"] = str(page_size)
        out: list = []
        start = 0
        while True:
            q["start"] = str(start)
            if raw:
                resp = self.raw("GET", path, q)
                try:
                    page = json.loads(resp.body, object_pairs_hook=_RawObject)
                except ValueError as e:
                    raise Error(f"decoding the response of GET {self.url(path, q)}: {e}") from None
            else:
                page = self.do("GET", path, q) or {}
            values = page.get("values") or []
            for v in values:
                if keep is not None and not keep(v):
                    continue
                out.append(v)
                if limit > 0 and len(out) >= limit:
                    return out
            if page.get("isLastPage") or not values:
                return out
            start = int(page.get("nextPageStart") or 0)

    def current_user(self) -> str:
        """The authenticated user's slug."""
        if self.user_slug:
            return self.user_slug
        u = self.whoami()
        self.user_slug = u.get("slug", "")
        return self.user_slug

    def whoami(self) -> dict:
        """The authenticated user, via the applinks whoami servlet, falling back
        to the X-AUSERNAME header."""
        name = ""
        try:
            name = self.raw("GET", "plugins/servlet/applinks/whoami", None, [("Accept", "text/plain")]).text().strip()
        except Error:
            pass
        if not name:
            resp = self.raw("GET", "rest/api/1.0/application-properties")
            name = resp.header("X-AUSERNAME")
        if not name:
            raise APIError(401, "GET", self.base_url, ["token not accepted (anonymous access)"])
        return self.resolve_user(name)

    def resolve_user(self, q: str) -> dict:
        """Map a user-supplied identifier (slug, username, email, display name
        fragment) to a Bitbucket user."""
        q = q.removeprefix("@")
        try:
            return self.do("GET", "rest/api/1.0/users/" + path_escape(q)) or {}
        except APIError as e:
            if e.status != 404:
                raise
        users = self.paginate("rest/api/1.0/users", {"filter": q}, 25)
        for cand in users:
            if q.lower() in (
                str(cand.get("name", "")).lower(),
                str(cand.get("slug", "")).lower(),
                str(cand.get("emailAddress", "")).lower(),
            ):
                return cand
        if not users:
            raise Error(f"no user matching {json.dumps(q)}")
        if len(users) == 1:
            return users[0]
        names = ", ".join(f"{c.get('slug', '')} ({c.get('displayName', '')})" for c in users)
        raise Error(f"{json.dumps(q)} is ambiguous, candidates: {names}")

    # Repositories and pull requests

    def get_repo(self, project: str, slug: str) -> dict:
        return self.do("GET", repo_path(project, slug)) or {}

    def default_branch(self, project: str, slug: str) -> str:
        try:
            ref = self.do("GET", repo_path(project, slug) + "/default-branch") or {}
        except APIError as e:
            if e.status != 404:
                raise
            ref = self.do("GET", repo_path(project, slug) + "/branches/default") or {}  # pre-7.x endpoint
        return ref.get("displayId", "")

    def branch_exists(self, project: str, slug: str, branch: str) -> bool:
        found = self.paginate(
            repo_path(project, slug) + "/branches",
            {"filterText": branch, "boostMatches": "true"},
            1,
            lambda r: r.get("displayId") == branch,
        )
        return bool(found)

    def get_pr(self, project: str, slug: str, pr_id: int) -> dict:
        return self.do("GET", pr_path(project, slug, pr_id)) or {}

    def comments(self, project: str, slug: str, pr_id: int) -> list[dict]:
        """The PR's comment threads (root comments with nested replies), oldest
        first, with anchors filled in from activities."""
        acts = self.paginate(
            pr_path(project, slug, pr_id) + "/activities",
            None,
            0,
            lambda a: a.get("action") == "COMMENTED" and a.get("comment") is not None,
        )
        nested: set = set()

        def mark(cs: list) -> None:
            for cm in cs or []:
                nested.add(cm.get("id"))
                mark(cm.get("comments"))

        for a in acts:
            mark(a["comment"].get("comments"))
        # Activities are newest first; walk backwards so roots end up oldest
        # first, but keep the newest copy of each root (latest replies/edits).
        latest: dict = {}
        for a in acts:
            cid = a["comment"].get("id")
            if cid not in latest:
                cm = dict(a["comment"])
                if cm.get("anchor") is None:
                    cm["anchor"] = a.get("commentAnchor")
                latest[cid] = cm
        roots, seen = [], set()
        for a in reversed(acts):
            cid = a["comment"].get("id")
            if cid in nested or cid in seen:
                continue
            seen.add(cid)
            roots.append(latest[cid])
        return roots

    def changes(self, project: str, slug: str, pr_id: int) -> list[dict]:
        return self.paginate(pr_path(project, slug, pr_id) + "/changes", {"withComments": "false"})

    def build_statuses(self, commit: str) -> list[dict]:
        return self.paginate("rest/build-status/1.0/commits/" + path_escape(commit))

    def merge_status(self, project: str, slug: str, pr_id: int) -> dict:
        return self.do("GET", pr_path(project, slug, pr_id) + "/merge") or {}

    def raw_diff(self, project: str, slug: str, pr_id: int, patch: bool) -> str:
        """The PR's unified diff; falls back to rendering the JSON diff on
        servers without the .diff endpoint."""
        suffix = ".patch" if patch else ".diff"
        try:
            return self.raw("GET", pr_path(project, slug, pr_id) + suffix, None, [("Accept", "text/plain")]).text()
        except APIError as e:
            if e.status != 404 or patch:
                raise
        return unified(self.pr_diff(project, slug, pr_id, "", 3))

    def pr_diff(self, project: str, slug: str, pr_id: int, path: str, context_lines: int) -> dict:
        """The PR's structured diff, optionally for a single path. context_lines
        < 0 uses the server default, which also decides where comments can go."""
        return self.do("GET", pr_path(project, slug, pr_id) + "/diff" + path_suffix(path), diff_query(context_lines)) or {}

    def compare_diff(self, project: str, slug: str, frm: str, to: str, path: str, context_lines: int) -> dict:
        """The diff of changes in frm that are not in to."""
        q = diff_query(context_lines)
        q["from"], q["to"] = frm, to
        return self.do("GET", repo_path(project, slug) + "/compare/diff" + path_suffix(path), q) or {}

    def pr_commits(self, project: str, slug: str, pr_id: int) -> list[dict]:
        return self.paginate(pr_path(project, slug, pr_id) + "/commits")

    def get_comment(self, project: str, slug: str, pr_id: int, comment_id: int) -> dict:
        return self.do("GET", pr_path(project, slug, pr_id) + f"/comments/{comment_id}") or {}

    def raw_file(self, project: str, slug: str, path: str, at: str) -> str:
        """A file's content at a commit."""
        q = {"at": at} if at else None
        return self.raw("GET", repo_path(project, slug) + "/raw" + path_suffix(path), q, [("Accept", "text/plain")]).text()


class _RawObject(Struct):
    """Objects decoded for `api --paginate`, re-encoded in the server's key order."""

    def __init__(self, pairs: list) -> None:
        super().__init__(pairs)


def _version(r: Any) -> str:
    return {10: "HTTP/1.0", 11: "HTTP/1.1"}.get(getattr(r, "version", 11), "HTTP/1.1")


def parse_error(resp: Response, method: str, url: str) -> APIError:
    data = resp.body[: 1 << 20]
    messages: list[str] = []
    try:
        payload = json.loads(data)
        if isinstance(payload, dict):
            for m in payload.get("errors") or []:
                if isinstance(m, dict):
                    messages.append(str(m.get("message", "")))
    except ValueError:
        pass
    if not messages and 0 < len(data) < 500 and b"<html" not in data:
        messages = [data.decode("utf-8", "replace").strip()]
    return APIError(resp.status, method, url, messages)


def repo_path(project: str, slug: str) -> str:
    """The REST path prefix for a repository."""
    return f"rest/api/1.0/projects/{path_escape(project)}/repos/{path_escape(slug)}"


def pr_path(project: str, slug: str, pr_id: int) -> str:
    return f"{repo_path(project, slug)}/pull-requests/{pr_id}"


def path_suffix(path: str) -> str:
    if not path:
        return ""
    return "/" + "/".join(path_escape(p) for p in path.strip("/").split("/"))


def diff_query(context_lines: int) -> dict[str, Any]:
    q: dict[str, Any] = {"withComments": "false"}
    if context_lines >= 0:
        q["contextLines"] = str(context_lines)
    return q


# Object helpers (Go's methods on the API types)


def links_href(obj: dict, rel: str) -> str:
    links = (obj.get("links") or {}).get(rel) or []
    return links[0].get("href", "") if links else ""


def web_url(obj: dict) -> str:
    """A repository's or pull request's web URL."""
    return links_href(obj, "self")


def clone_url(repo: dict, name: str) -> str:
    for link in (repo.get("links") or {}).get("clone") or []:
        if link.get("name") == name:
            return link.get("href", "")
    return ""


def full_name(repo: dict) -> str:
    return (repo.get("project") or {}).get("key", "") + "/" + repo.get("slug", "")


def ref(pr: dict, which: str) -> dict:
    """pr["fromRef"] / pr["toRef"], {} if missing."""
    return pr.get(which) or {}


def is_cross_repository(pr: dict) -> bool:
    fr, tr = ref(pr, "fromRef").get("repository"), ref(pr, "toRef").get("repository")
    return fr is not None and tr is not None and fr.get("id", 0) != tr.get("id", 0)


def user_of(participant: dict | None) -> dict:
    return (participant or {}).get("user") or {}


# Diffs


def diff_path(d: dict) -> str:
    """The file's current path (or its old path if deleted)."""
    if d.get("destination") is not None:
        return d["destination"].get("toString", "")
    if d.get("source") is not None:
        return d["source"].get("toString", "")
    return ""


def diff_old_path(d: dict) -> str:
    """The file's previous path, if it existed."""
    if d.get("source") is not None:
        return d["source"].get("toString", "")
    return ""


def change_type(d: dict) -> str:
    if d.get("source") is None:
        return "ADD"
    if d.get("destination") is None:
        return "DELETE"
    if d["source"].get("toString", "") != d["destination"].get("toString", ""):
        return "RENAME"
    return "MODIFY"


def unified(resp: dict) -> str:
    """Render a structured diff in git's unified format."""
    return "".join(unified_file(f) for f in resp.get("diffs") or [])


def unified_file(f: dict) -> str:
    b = []
    src = "a/" + f["source"].get("toString", "") if f.get("source") is not None else "/dev/null"
    dst = "b/" + f["destination"].get("toString", "") if f.get("destination") is not None else "/dev/null"
    old = diff_old_path(f) or diff_path(f)
    b.append(f"diff --git a/{old} b/{diff_path(f)}\n")
    if f.get("binary"):
        b.append(f"Binary files {src} and {dst} differ\n")
        return "".join(b)
    b.append(f"--- {src}\n+++ {dst}\n")
    for h in f.get("hunks") or []:
        b.append(f"@@ -{h.get('sourceLine', 0)},{h.get('sourceSpan', 0)} +{h.get('destinationLine', 0)},{h.get('destinationSpan', 0)} @@\n")
        for s in h.get("segments") or []:
            prefix = {"ADDED": "+", "REMOVED": "-"}.get(s.get("type", ""), " ")
            for line in s.get("lines") or []:
                b.append(prefix + line.get("line", "") + "\n")
    return "".join(b)

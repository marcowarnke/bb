"""Per-host credentials, modelled after gh's hosts.yml (same config.json as the Go version)."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlsplit

from . import credstore
from .cli import Error


class NoHostError(Error):
    pass


def err_no_host() -> NoHostError:
    return NoHostError("no Bitbucket host configured: run `bb auth login --hostname <host>` or set BB_HOST and BB_TOKEN")


@dataclass
class Host:
    base_url: str = ""
    token: str = ""
    user: str = ""
    git_protocol: str = ""
    # Where resolve found the token: "BB_TOKEN", "keyring" or the config path.
    token_source: str = ""

    def to_json(self) -> dict:
        d = {"base_url": self.base_url}
        if self.token:
            d["token"] = self.token
        if self.user:
            d["user"] = self.user
        if self.git_protocol:
            d["git_protocol"] = self.git_protocol
        return d

    def copy(self) -> Host:
        return Host(self.base_url, self.token, self.user, self.git_protocol)


def config_dir() -> Path:
    if d := os.environ.get("BB_CONFIG_DIR"):
        return Path(d)
    if d := os.environ.get("XDG_CONFIG_HOME"):
        return Path(d) / "bb"
    return Path.home() / ".config" / "bb"


def path() -> Path:
    return config_dir() / "config.json"


def service(host: str) -> str:
    return "bb:" + host


def keyring_get(host: str, user: str) -> str:
    return credstore.get(service(host), user)


def keyring_set(host: str, user: str, token: str) -> None:
    credstore.set(service(host), user, token)


def keyring_delete(host: str, user: str) -> None:
    credstore.delete(service(host), user)


def normalize_host(raw: str) -> tuple[str, str]:
    """Turn "bitbucket.corp", "https://bitbucket.corp/ctx/" etc. into a host
    key ("bitbucket.corp") and base URL ("https://bitbucket.corp/ctx")."""
    raw = raw.strip()
    if not raw:
        raise Error("empty hostname")
    if "://" not in raw:
        raw = "https://" + raw
    try:
        u = urlsplit(raw)
    except ValueError:
        raise Error(f"invalid hostname {json.dumps(raw)}") from None
    host = hostname(u.netloc)
    if not host:
        raise Error(f"invalid hostname {json.dumps(raw)}")
    return host, (u.scheme + "://" + u.netloc + u.path).rstrip("/")


def hostname(netloc: str) -> str:
    """The host of a URL's netloc without userinfo and port, case kept (Go's URL.Hostname)."""
    host = netloc.rpartition("@")[2]
    if host.startswith("["):
        return host[1 : host.find("]")] if "]" in host else ""
    return host.partition(":")[0]


class Config:
    def __init__(self, hosts: dict[str, Host], file: Path):
        self.hosts = hosts
        self.path = file

    @classmethod
    def load(cls) -> Config:
        p = path()
        try:
            data = p.read_text(encoding="utf-8")
        except FileNotFoundError:
            return cls({}, p)
        try:
            raw = json.loads(data)
        except ValueError as e:
            raise Error(f"parsing {p}: {e}") from None
        hosts = {}
        for k, v in (raw.get("hosts") or {}).items():
            hosts[k] = Host(
                base_url=v.get("base_url", ""), token=v.get("token", ""), user=v.get("user", ""), git_protocol=v.get("git_protocol", "")
            )
        return cls(hosts, p)

    def save(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        data = json.dumps({"hosts": {k: self.hosts[k].to_json() for k in sorted(self.hosts)}}, indent=2) + "\n"
        fd = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(data)

    def host_names(self) -> list[str]:
        return sorted(self.hosts)

    def resolve(self, name: str) -> tuple[str, Host]:
        """Pick the host to use. Precedence: explicit name, BB_HOST, the only
        configured host. BB_TOKEN overrides the stored token."""
        key, h = self.resolve_host(name)
        t = os.environ.get("BB_TOKEN", "")
        if t:
            h.token, h.token_source = t, "BB_TOKEN"
        elif h.token:
            h.token_source = str(self.path)
        elif h.user:
            try:
                tok = keyring_get(key, h.user)
            except credstore.NotFound:
                tok = ""
            except Exception as e:
                raise ResolveError(key, h, f"reading token for {key} from the OS keyring: {e}") from e
            h.token, h.token_source = tok, "keyring"
        if not h.token:
            raise ResolveError(key, h, f"not logged in to {key}: run `bb auth login --hostname {key}` or set BB_TOKEN")
        return key, h

    def resolve_host(self, name: str) -> tuple[str, Host]:
        if not name:
            name = os.environ.get("BB_HOST", "")
        if name:
            key, base = normalize_host(name)
            stored = self.hosts.get(key)
            return key, stored.copy() if stored else Host(base_url=base)
        if not self.hosts:
            raise err_no_host()
        if len(self.hosts) > 1:
            raise Error(f"multiple hosts configured ({', '.join(self.host_names())}); set BB_HOST or pass --hostname/-R HOST/PROJECT/REPO")
        key = next(iter(self.hosts))
        return key, self.hosts[key].copy()


class ResolveError(Error):
    """A host was found but has no usable token; key and host are kept (Go returns them with the error)."""

    def __init__(self, key: str, host: Host, msg: str):
        super().__init__(msg)
        self.key, self.host = key, host

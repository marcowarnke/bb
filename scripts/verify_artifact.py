"""Verify the built artifacts in dist/ end to end: every launcher this OS can
run, against an in-process fake Bitbucket. Checks the version, output
(including non-ASCII), stdin, exit codes, argument quoting, the jq
integration and the skills embedded in the .pyz.

Usage: uv run python scripts/verify_artifact.py [--no-uv] [dist]

--no-uv hides uv from the launchers, so they fall back to the Python on PATH.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "tests"))
from fakes import PR_BASE, FakeBB  # noqa: E402

APP = "bb"


def launchers(dist: Path) -> list[tuple[str, list[str], str]]:
    """(name, command prefix, mode) for every way this OS runs the CLI. Modes: argv
    (run the prefix with the arguments), cmd (typed at a cmd.exe prompt), ps (typed
    at a PowerShell prompt); the last two find the launcher on PATH."""
    out = []
    if os.name == "nt":
        out.append(("cmd (direct)", [str(dist / f"{APP}.cmd")], "argv"))
        out.append(("cmd (on PATH)", [], "cmd"))
        for ps in ("powershell", "pwsh"):
            exe = shutil.which(ps)
            if exe:
                out.append((f"{ps} (on PATH, .ps1)", [exe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command"], "ps"))
        sh = shutil.which("sh")
        if sh:
            out.append(("sh (Git Bash)", [sh, str(dist / APP)], "argv"))
    else:
        out.append(("sh", [str(dist / APP)], "argv"))
    out.append(("pyz", [sys.executable, str(dist / f"{APP}.pyz")], "argv"))
    return out


def cmd_quote(a: str) -> str:
    """Quote one argument the way a person types it in cmd.exe."""
    if a and not re.search(r'[\s"&|<>^()]', a):
        return a
    return '"' + a.replace('"', '\\"') + '"'


def ps_quote(a: str) -> str:
    """Quote one argument the way a person types it in PowerShell (a,b stays unquoted)."""
    if re.fullmatch(r"[A-Za-z0-9_./:=,+-][A-Za-z0-9_./:@=,+-]*", a):
        return a
    return "'" + a.replace("'", "''") + "'"


class Runner:
    def __init__(self, prefix: list[str], mode: str, env: dict[str, str]):
        self.prefix, self.mode, self.env = prefix, mode, env
        self.results: list[tuple[str, bool, object]] = []

    def run(self, *args: str, stdin: str = "", extra: dict | None = None) -> tuple[str, str, int]:
        env = dict(self.env, **(extra or {}))
        if self.mode == "cmd":
            line = " ".join([APP, *(cmd_quote(a) for a in args)])
            p = subprocess.run(f'cmd /d /s /c "{line}"', input=stdin.encode(), capture_output=True, env=env)
        elif self.mode == "ps":  # stdin comes from a PowerShell pipeline, as a user would write it
            line = " ".join([APP, *(ps_quote(a) for a in args)])
            if stdin:
                line = "'" + stdin.rstrip("\n").replace("'", "''") + "' | " + line  # a string, never a command
            p = subprocess.run(self.prefix + [line + "; exit $LASTEXITCODE"], stdin=subprocess.DEVNULL, capture_output=True, env=env)
        else:
            p = subprocess.run(self.prefix + list(args), input=stdin.encode(), capture_output=True, env=env)

        def text(b: bytes) -> str:
            return b.decode("utf-8", "replace").replace("\r\n", "\n")

        return text(p.stdout), text(p.stderr), p.returncode

    def check(self, what: str, ok: bool, detail: object = "") -> None:
        self.results.append((what, ok, detail))


def verify(r: Runner, fake: FakeBB, version: str, jq: bool) -> None:
    out, err, code = r.run("--version")
    r.check("version", code == 0 and out == f"{APP} version {version}\n", (out, err, code))
    out, err, code = r.run("pr", "list", "-R", "PROJ/repo")
    r.check("plain output", code == 0 and out.startswith("7\tAdd feature\tfeat\tOPEN\t"), (out, err))
    out, err, code = r.run("pr", "view", "7", "-R", "PROJ/repo", "--comments")
    r.check("non-ASCII output", code == 0 and "  ↳ alice" in out, (out[-300:], err))
    out, err, code = r.run("pr", "comment", "7", "-R", "PROJ/repo", "-F", "-", stdin="body from stdin\n")
    r.check("stdin", code == 0 and fake.bodies.get(f"POST {PR_BASE}/7/comments", {}).get("text") == "body from stdin", (out, err))
    out, err, code = r.run("pr", "list", "-R", "PROJ/repo", "--json", "number,title", "--format", "#{number} {title} (ok)")
    r.check("argument quoting", code == 0 and out == "#7 Add feature (ok)\n", (out, err))
    if jq:
        out, err, code = r.run("pr", "list", "-R", "PROJ/repo", "--json", "number,title", "--jq", '.[] | "\\(.number): \\(.title)"')
        r.check("--jq with quotes", code == 0 and out == "7: Add feature\n", (out, err))
    _, err, code = r.run("pr", "checks", "7", "-R", "PROJ/repo")
    r.check("exit code 8 (pending)", code == 8, (err, code))
    _, err, code = r.run("nope")
    r.check("exit code 1", code == 1 and 'unknown command "nope"' in err, (err, code))
    _, err, code = r.run("pr", "view", "7", "-R", "PROJ/repo", extra={"BB_TOKEN": "wrong"})
    r.check("exit code 4 (auth)", code == 4 and "HTTP 401" in err, (err, code))
    out, err, code = r.run("skills", "list")
    r.check("embedded skills", code == 0 and out.count("\n") == 3 and out.startswith("bb\t"), (out, err))


def main() -> int:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")  # Windows pipes default to cp1252
    ap = argparse.ArgumentParser()
    ap.add_argument("dist", nargs="?", default=str(ROOT / "dist"))
    ap.add_argument("--no-uv", action="store_true")
    opts = ap.parse_args()
    dist = Path(opts.dist).resolve()
    version = re.search(r'^version = "(.+)"$', (ROOT / "pyproject.toml").read_text(encoding="utf-8"), re.M).group(1)
    if os.name != "nt":
        (dist / APP).chmod(0o755)
    env = {k: v for k, v in os.environ.items() if not k.startswith("BB_") and k != "NO_COLOR"}
    path = env.get("PATH", "")
    if opts.no_uv:
        uv = "uv.exe" if os.name == "nt" else "uv"
        path = os.pathsep.join(d for d in path.split(os.pathsep) if not (Path(d) / uv).exists())
    env["PATH"] = str(dist) + os.pathsep + path
    jq = shutil.which("jq", path=env["PATH"]) is not None

    failures = 0
    for name, prefix, mode in launchers(dist):
        fake = FakeBB()
        tmp = tempfile.mkdtemp()
        r = Runner(prefix, mode, dict(env, BB_CONFIG_DIR=tmp, BB_HOST=fake.url, BB_TOKEN="secret", HOME=tmp, USERPROFILE=tmp))
        verify(r, fake, version, jq)
        if mode == "ps":
            # The PowerShell completion script, through the launcher on PATH.
            matches = f"(TabExpansion2 -inputScript '{APP} pr vi' -cursorColumn 8).CompletionMatches"
            script = f"{APP} completion powershell | Out-String | Invoke-Expression; {matches} | ForEach-Object {{ $_.CompletionText }}"
            p = subprocess.run([prefix[0], "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script], capture_output=True, env=r.env)
            out = p.stdout.decode("utf-8", "replace").split()
            r.check("powershell completion", out == ["view"], (out, p.stderr.decode("utf-8", "replace")[-300:]))
        fake.close()
        shutil.rmtree(tmp, ignore_errors=True)
        bad = [c for c in r.results if not c[1]]
        failures += len(bad)
        print(f"{name}: {len(r.results) - len(bad)}/{len(r.results)} checks passed")
        for what, _, detail in bad:
            print(f"  FAIL {what}: {detail!r}")
    print("OK" if failures == 0 else f"{failures} check(s) failed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

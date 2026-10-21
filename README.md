# bb — `gh`-style CLI for Bitbucket Data Center

Same command tree, flags, and output conventions as the GitHub CLI, targeting the Bitbucket DC REST API (`/rest/api/1.0`). Plain Python (3.10+, standard library plus truststore), shipped as a single zip app with launchers, so there is no binary for endpoint policies to flag.

## Install

Needs [uv](https://docs.astral.sh/uv/) (recommended: it also provides Python) or Python 3.10+. `--jq` needs the [jq](https://jqlang.org/download/) command; without it, use `--format`.

1. Download `bb-v0.6.0.zip` from [Releases](https://github.com/marcowarnke/bb/releases) and unpack it. The `bb` folder holds:

   | File | What |
   |---|---|
   | `bb.pyz` | The whole CLI as one Python zip app |
   | `bb.cmd` | Launcher for cmd.exe (and PowerShell) on Windows |
   | `bb.ps1` | Launcher for PowerShell on Windows |
   | `bb` | Launcher for macOS/Linux shells and Git Bash |

2. Put the folder on your `PATH` and check with `bb --version`:

   ```powershell
   # Windows (PowerShell), for the current user
   $dir = "$HOME\bin\bb"   # where you unpacked it
   [Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$dir", "User")
   ```

   ```sh
   # macOS/Linux
   ln -s "$PWD/bb/bb" ~/.local/bin/bb
   ```

The launchers run `bb.pyz` through `uv run` when uv is installed (uv finds or downloads a suitable Python), otherwise with `python3`/`python` (or `py -3` on Windows) from `PATH`. No executable is installed.

On Windows, PowerShell prefers `bb.ps1` over `bb.cmd`. If PowerShell says running scripts is disabled (execution policy), delete `bb.ps1` and `bb.cmd` takes over.

Alternatively, install it as a uv tool. On Windows this creates a small `bb.exe` shim, which endpoint policies may block like the old binaries:

```sh
uv tool install git+https://github.com/marcowarnke/bb@v0.6.0
```

TLS: `bb` trusts the operating system's certificate store (through [truststore](https://github.com/sethmlarson/truststore)), so corporate TLS inspection works without setup; `SSL_CERT_FILE` overrides it. For uv itself (downloading Python behind such a proxy), set `UV_SYSTEM_CERTS=1`.

### Upgrading from the Go binaries

Delete the old `bb` executable and install as above. Logins carry over: keyring entries and `config.json` are read and written exactly as before (CI checks the keyring format against go-keyring on every OS). Re-run `bb skills install` afterwards.

The command line is unchanged, except that Go's `--template` is replaced by `--format`, a Python format string applied to each item (`--json number,title --format '#{number} {title}'`).

## Auth

```sh
bb auth login --hostname bitbucket.example.com            # prompts for HTTP access token
echo "$TOKEN" | bb auth login -h https://host/ctx --with-token   # include context path if any
bb auth status | token | logout
```

Token = DC HTTP access token, repo write scope. Storage follows gh:

| Where | When |
|---|---|
| OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret Service), service `bb:<host>` | Default |
| `~/.config/bb/config.json`, plain text, 0600 | `--insecure-storage`, or no keyring available (e.g. headless Linux) — login warns |
| `BB_TOKEN` | Always wins; nothing stored. Use for CI/agent sandboxes |

Both stored forms survive reboots; `bb auth status` shows which one is in use, `bb auth logout` removes it. Config dir honours `BB_CONFIG_DIR`/`XDG_CONFIG_HOME`.

| Env | Effect |
|---|---|
| `BB_TOKEN` | Overrides stored token (CI/agents) |
| `BB_HOST` | Host or base URL; used when the git remote host isn't configured |
| `BB_REPO` | Default `PROJECT/REPO` (same as `-R`) |
| `NO_COLOR`, `BROWSER` | As in gh |

Repo resolution: `-R [HOST/]PROJECT/REPO` (or URL) → `BB_REPO` → git remotes (`upstream` > `origin` > others). Handles `/scm/` HTTPS, SSH `:7999`, `~user` personal repos.

## Shell completion

```sh
bb completion zsh > "${fpath[1]}/_bb"          # zsh
bb completion bash > /etc/bash_completion.d/bb  # bash (needs bash-completion v2)
bb completion fish > ~/.config/fish/completions/bb.fish
bb completion powershell | Out-String | Invoke-Expression   # PowerShell; add to your $PROFILE
```

Completion is live: PR arguments complete to open PR numbers with `title · author · branch` (declined ones for `reopen`), reviewer/author flags to users, `-B/-H/-b` to branches, `-R` to `PROJECT/REPO`. It uses the same repo resolution as the command and stays silent if the server is unreachable (5 s timeout).

## Commands

| Command | Notes |
|---|---|
| `pr list` | `-s open\|closed\|merged\|all`, `-A/--author`, `--reviewer`, `-B`, `-H`, `-S`, `-d`, `-L` |
| `pr view` | `-c/--comments` shows threads with comment IDs and `file:line` anchors; open PRs show whether they can merge and what blocks them |
| `pr status` | Current branch / created by you / review requested |
| `pr diff` | Unified diff; `--name-only`, `--patch`, `--color`; agent options: `-n` (explicit old/new line numbers), `--json`, `--stat`, `-- <paths>`, `--exclude`, `--max-lines`, `--since-last-review`, `--since <sha>` |
| `pr threads` | Open tasks/threads with IDs, anchors, code context, replies; `--needs-reply`, `--started-by`, `--all`, `--json` |
| `pr checks` | Build statuses of head commit; `--watch`; exit 1 fail, 8 pending |
| `pr checkout` | Same-repo and fork PRs (`refs/pull-requests/N/from`) |
| `pr create` | `-t -b -F -B -H -r -d --fill --fill-first --dry-run`; adds default reviewers unless `--no-default-reviewers` |
| `pr edit` | `-t -b -F -B --add-reviewer --remove-reviewer` |
| `pr ready [--undo]` | Draft toggle (DC 8.18+) |
| `pr comment` | gh flags + `--path --line` (validated against the diff, line type inferred), `--line-type removed`, `--suggestion`, `--task`, `--reply-to ID [--resolve\|--reopen]` |
| `pr review` | `--approve` → Approved, `--request-changes` → Needs work, `--comment`; `--input review.json [--dry-run]` posts a whole validated review |
| `pr merge` | `--merge`(no-ff) / `--squash` / `--rebase`(rebase-no-ff), default = repo default; `-d`, `-t`, `-b`; `--dry-run` only checks: lists conflicts and merge-check vetoes (approvals, builds, tasks) |
| `pr close` / `reopen` | Decline / reopen; `-c` comment, `close -d` deletes branch |
| `status` | Dashboard across all repos: your open PRs with merge readiness / blockers, PRs awaiting your review, PRs where you requested changes. `--json` (adds `repository`), `-L`, `--hostname` |
| `browse` | Open repo / PR number / commit / `path[:line[-line]]` in the browser; `-b`, `-c`, `-s` settings, `-p` pull requests, `-n` print URL. Paths are relative to the current dir; files open on the pushed current branch, else the default branch |
| `repo view` / `clone` | `view` prints README; `clone` honours `git_protocol` |
| `api` | Raw REST: `-X -f -F -H --input --paginate --jq -i`; `{project}` `{repo}` `{branch}` placeholders; non-`rest/` paths get `rest/api/1.0/` |

PR arguments: number, URL, or branch; omitted = current branch.
`--json fields` / `--jq` / `--format` on `pr list|view|status|checks|threads`, `repo view`; `--json` alone lists fields. `--jq` runs the `jq` command (`-r -c`, like gh); `--format '#{number} {title}'` is a Python format string applied to each item, one line each (lists are joined with commas, objects printed as JSON). `pr view`/`list` add `commits`, `unresolvedThreads`, `mergeStateStatus` (`CLEAN`/`BLOCKED`/`DIRTY`/`DRAFT`/`UNKNOWN`) and `mergeBlockers` fields.

### gh → Bitbucket mapping

| gh | Bitbucket DC |
|---|---|
| `CLOSED` | `DECLINED` |
| `reviewDecision` `CHANGES_REQUESTED` | any reviewer `NEEDS_WORK` |
| `reviewRequests` / `latestReviews` | `reviewers[]` / their status |
| `statusCheckRollup` | build-status for head commit |
| assignees, labels, milestones, projects | n/a in Bitbucket |

Reviewer values resolve as slug → username → email → unique `users?filter=` match; ambiguous matches error with candidates.

## Agent usage

Non-TTY output is tab-separated/plain, no color, no prompts (missing required input → error). Prefer `--json` + `--jq` (or `--format` without jq). Exit codes: 0 ok, 1 error, 4 auth, 8 checks pending.

Two loops, each shipped as a Claude Code skill in `src/bb/skills/`:

| Skill | Loop |
|---|---|
| `bb` | Command reference, conventions |
| `bb-review` | view → diff `--stat` → diff `-n` per path → `review.json` → `review --input --dry-run` → submit → report. Re-review: `threads --started-by @me --needs-reply` → verify/resolve → `diff --since-last-review` |
| `bb-address-feedback` | `list --author @me --json unresolvedThreads` → `checkout` → `threads --json` → fix/commit/push → `comment --reply-to ID [--resolve]` → `threads --needs-reply` + `checks --watch` → report |

Review loop core:

```sh
bb pr diff 42 -n -- src/api/                      # OLD NEW MARK text
bb pr review 42 --input review.json --dry-run      # validates every anchor, posts nothing
bb pr review 42 --input review.json                # comments → summary → status
```

Feedback loop core:

```sh
bb pr threads 42 --json id,kind,path,line,body,replies,context
bb pr comment 42 --reply-to 1234 -b "Fixed in 3f2a1c9" --resolve
```

Install the skills (embedded in `bb.pyz`, so they always match its flags; re-run after upgrading). In a terminal, `bb skills install` asks where, showing which locations are installed / outdated:

| `--agent` | `--scope user` | `--scope project` |
|---|---|---|
| `claude` | `~/.claude/skills` | `.claude/skills` |
| `copilot` | `~/.copilot/skills` | `.github/skills` |
| `agents` | `~/.agents/skills` | `.agents/skills` |

```sh
bb skills install                                   # interactive (piped: ~/.claude/skills)
bb skills install --agent copilot --scope project   # .github/skills
bb skills install --agent claude,copilot --scope user,project
bb skills install --dir path/to/skills
bb skills list
```

Copilot also reads the Claude and `.agents` directories, so one location per scope is enough for both.

## Dev

```sh
uv sync                                     # environment with the dev tools
uv run bb --help                            # run from the checkout
uv run pytest                               # tests (need jq)
uv run ruff check && uv run ruff format --check
uv run python scripts/build.py              # → dist/: bb.pyz, bb.cmd, bb.ps1, bb, bb-vX.Y.Z.zip
uv run python scripts/verify_artifact.py    # run the built launchers against a fake server
```

CI (`.github/workflows/build.yml`) runs on every push: lint, tests on Linux, macOS and Windows with Python 3.10 and 3.13, a keyring check against go-keyring, the build, and the built launchers on every OS with and without uv. Pushing a `v*` tag that matches the version in `pyproject.toml` publishes a release with the files.

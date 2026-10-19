# bb — `gh`-style CLI for Bitbucket Data Center

Same command tree, flags, and output conventions as the GitHub CLI, targeting the Bitbucket DC REST API (`/rest/api/1.0`). Single static binary, no runtime deps.

## Install

Download a binary from [Releases](https://github.com/marcowarnke/bb/releases) (windows/amd64, linux/amd64, darwin/arm64), or:

```sh
go install github.com/marcowarnke/bb@latest
make install                 # from a checkout, with version stamp
make dist                    # → dist/bb_<os>_<arch>
```

CI (`.github/workflows/build.yml`) tests and builds on every push (binaries as workflow artifact `bb-binaries`); pushing a `v*` tag publishes a release with the binaries.

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

## Commands

| Command | Notes |
|---|---|
| `pr list` | `-s open\|closed\|merged\|all`, `-A/--author`, `--reviewer`, `-B`, `-H`, `-S`, `-d`, `-L` |
| `pr view` | `-c/--comments` shows threads with comment IDs and `file:line` anchors |
| `pr status` | Current branch / created by you / review requested |
| `pr diff` | Unified diff; `--name-only`, `--patch`, `--color` |
| `pr checks` | Build statuses of head commit; exit 1 fail, 8 pending |
| `pr checkout` | Same-repo and fork PRs (`refs/pull-requests/N/from`) |
| `pr create` | `-t -b -F -B -H -r -d --fill --fill-first --dry-run`; adds default reviewers unless `--no-default-reviewers` |
| `pr edit` | `-t -b -F -B --add-reviewer --remove-reviewer` |
| `pr ready [--undo]` | Draft toggle (DC 8.18+) |
| `pr comment` | gh flags + `--path --line --line-type added\|removed\|context --reply-to ID --task` |
| `pr review` | `--approve` → Approved, `--request-changes` → Needs work, `--comment`; `-b` posts a comment |
| `pr merge` | `--merge`(no-ff) / `--squash` / `--rebase`(rebase-no-ff), default = repo default; `-d`, `-t`, `-b` |
| `pr close` / `reopen` | Decline / reopen; `-c` comment, `close -d` deletes branch |
| `repo view` / `clone` | `view` prints README; `clone` honours `git_protocol` |
| `api` | Raw REST: `-X -f -F -H --input --paginate --jq -i`; `{project}` `{repo}` `{branch}` placeholders; non-`rest/` paths get `rest/api/1.0/` |

PR arguments: number, URL, or branch; omitted = current branch.
`--json fields` / `--jq` / `--template` on `pr list|view|status|checks`, `repo view`; `--json` alone lists fields.

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

Non-TTY output is tab-separated/plain, no color, no prompts (missing required input → error). Prefer `--json` + `--jq`. Exit codes: 0 ok, 1 error, 4 auth, 8 checks pending.

Review loop:

```sh
bb pr view 42 --json title,body,headRefName,baseRefName,reviewDecision
bb pr diff 42 --name-only
bb pr diff 42
bb pr view 42 --comments
bb pr comment 42 -b "Null deref if cfg missing" --path svc/load.go --line 88
bb pr comment 42 -b "Done" --reply-to 1234
bb pr review 42 --request-changes -b "See inline comments"
```

See `skill/SKILL.md` for a drop-in Claude Code skill.

## Dev

`make test` — command tests run against an in-process fake DC server (`internal/cmd/cmd_test.go`).

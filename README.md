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
| `repo view` / `clone` | `view` prints README; `clone` honours `git_protocol` |
| `api` | Raw REST: `-X -f -F -H --input --paginate --jq -i`; `{project}` `{repo}` `{branch}` placeholders; non-`rest/` paths get `rest/api/1.0/` |

PR arguments: number, URL, or branch; omitted = current branch.
`--json fields` / `--jq` / `--template` on `pr list|view|status|checks|threads`, `repo view`; `--json` alone lists fields. `pr view`/`list` add `commits`, `unresolvedThreads`, `mergeStateStatus` (`CLEAN`/`BLOCKED`/`DIRTY`/`DRAFT`/`UNKNOWN`) and `mergeBlockers` fields.

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

Two loops, each shipped as a Claude Code skill in `skills/`:

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

Install the skills: `cp -r skills/* ~/.claude/skills/` (or a project's `.claude/skills/`).

## Dev

`make test` — command tests run against an in-process fake DC server (`internal/cmd/cmd_test.go`; loop scenarios in `loops_test.go`).

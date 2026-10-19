---
name: bb
description: Bitbucket Data Center CLI (`bb`, gh-compatible) reference. Use for any Bitbucket pull request or repository task. For reviewing a PR use the bb-review skill; for working through review feedback on your own PRs use bb-address-feedback.
---

`bb` mirrors `gh`: same subcommands and flags, against Bitbucket Data Center. Run `bb <cmd> --help` when unsure; help texts are written for agents.

Auth is preconfigured. `bb auth status` checks it; exit code 4 anywhere = not authenticated → stop and tell the user, never attempt login.

## Conventions

- PR argument: number, URL, or branch; omitted = PR of the current branch. `-R PROJECT/REPO` targets another repo.
- Prefer `--json <fields> [--jq <expr>]` for anything you parse. `--json` alone lists fields.
- Output is plain and prompt-free when piped. Exit codes: 0 ok, 1 error, 4 auth, 8 checks pending.
- Multi-line bodies: `-F -` with a quoted heredoc (`<<'EOF'`).
- Comment IDs come from `bb pr threads`; line numbers come from `bb pr diff -n`.

## Commands

| Need | Command |
|---|---|
| PR metadata | `bb pr view N --json number,title,body,author,headRefName,baseRefName,headRefOid,isDraft,reviewDecision,latestReviews,taskCount,unresolvedThreads,commits,url` |
| My / my review queue | `bb pr list --author @me` · `bb pr list --reviewer @me` · `bb pr status` |
| Diff size | `bb pr diff N --stat` |
| Diff with line numbers | `bb pr diff N -n [-- <path>...] [--exclude 'glob,…'] [--max-lines 1500]` |
| Structured diff | `bb pr diff N --json` |
| Only new changes | `bb pr diff N --since-last-review -n` · `--since <sha>` |
| Open feedback | `bb pr threads N [--needs-reply] [--started-by @me] [--all] [--json …]` |
| Comment | `bb pr comment N -b TEXT [--path F --line L [--line-type removed]] [--task] [--suggestion CODE]` |
| Reply / resolve | `bb pr comment N --reply-to ID [-b TEXT] [--resolve\|--reopen]` |
| Full review | `bb pr review N --input review.json [--dry-run]` |
| Verdict only | `bb pr review N --approve\|--request-changes\|--comment [-b TEXT]` |
| Builds | `bb pr checks N [--watch]` |
| Why can't it merge? | `bb pr merge N --dry-run` · `bb pr view N --json mergeStateStatus,mergeBlockers` |
| Create / update | `bb pr create -t T -F - -r alice,bob` · `bb pr edit N --add-reviewer x` |
| Check out | `bb pr checkout N` |
| File at a commit | `bb api -H 'Accept: text/plain' "projects/{project}/repos/{repo}/raw/<path>?at=<sha>"` |
| Anything else | `bb api <path> [--paginate] [--jq expr]` |

## Line numbers

`bb pr diff N -n` prints `OLD NEW MARK text`. Use NEW for `+` and ` ` lines; OLD with `--line-type removed` (`"lineType": "removed"`) for `-` lines. `bb` validates every line before posting and, if it's wrong, lists the lines that can take a comment. Never derive numbers from `@@` headers yourself.

## Creating a PR

```sh
git push -u origin HEAD
bb pr create -t "title" -F - -r alice,bob <<'EOF'
body
EOF
```

Repo default reviewers are added automatically. Reviewers accept slug, email, or a unique name fragment.

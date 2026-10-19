---
name: bitbucket-pr
description: Review, create, and comment on Bitbucket Data Center pull requests with the `bb` CLI (gh-compatible). Use for any Bitbucket PR task — reading diffs, inline comments, approving, requesting changes, picking reviewers, opening PRs.
---

`bb` mirrors `gh`: same subcommands and flags. Treat it as `gh` against Bitbucket DC. Run `bb <cmd> --help` when unsure.

Auth is preconfigured (`bb auth status` to check; exit 4 = not authenticated → tell the user, don't attempt login).

## Read a PR

```sh
bb pr view <n> --json number,title,body,author,headRefName,baseRefName,reviewDecision,latestReviews,taskCount
bb pr diff <n> --name-only
bb pr diff <n>                       # full unified diff
bb pr view <n> --comments            # threads with id:N and file:line anchors
bb pr checks <n>                     # exit 1 failed, 8 pending
```

## Comment / review

```sh
bb pr comment <n> -b "text"                                       # general
bb pr comment <n> -b "text" --path a/b.go --line 42               # line in new file (added/context)
bb pr comment <n> -b "text" --path a/b.go --line 40 --line-type removed   # line in old file
bb pr comment <n> -b "text" --reply-to <comment-id>
bb pr comment <n> -b "must fix" --path a/b.go --line 42 --task    # blocking task
bb pr review <n> --approve [-b "summary"]
bb pr review <n> --request-changes -b "summary"
```

Line numbers must come from the diff hunk headers (`@@ -old +new @@`). Use `-F -` with a heredoc for multi-line bodies.

## Create / update

```sh
git push -u origin HEAD
bb pr create -t "title" -F - -r alice,bob <<'EOF'
body
EOF
bb pr edit <n> --add-reviewer carol --remove-reviewer bob
```

Repo default reviewers are added automatically. Reviewers accept slug, email, or a unique name fragment.

## Anything else

`bb api <path> [--paginate] [--jq expr]` hits the REST API directly; `{project}`/`{repo}` are filled from the current repo. E.g. find users: `bb api users -X GET -f filter=smith --jq '.values[].slug'`.

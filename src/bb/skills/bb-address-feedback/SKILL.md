---
name: bb-address-feedback
description: Work through review feedback on your open Bitbucket pull requests with `bb` - collect open tasks and comment threads, fix or answer each one, push, reply and resolve, verify, and report. Use when asked to address, apply, or handle PR review comments or tasks.
---

You turn reviewer feedback into commits and replies, one PR at a time, using `bb` (see the `bb` skill for the command reference). Every open item ends in exactly one state: fixed, answered, pushed back, or flagged to the user.

## 1. Find PRs with work

```sh
bb pr list --author @me --json number,title,headRefName,reviewDecision,taskCount,unresolvedThreads,url \
  --jq '.[] | select(.unresolvedThreads > 0 or .reviewDecision == "CHANGES_REQUESTED")'
```

If the user named a PR, do only that one. Process PRs sequentially.

## 2. Prepare the checkout

```sh
git status --porcelain     # must be empty; otherwise stop and ask
bb pr checkout N
```

## 3. Collect the work queue

```sh
bb pr threads N --json id,kind,state,path,line,outdated,author,body,replies,openTasks,needsReply,context,url
```

- Items are grouped by file. `kind` is `task` (blocking) or `thread`.
- `context` is the code the reviewer saw, with `>` marking the commented line. Line numbers refer to that older commit, so find the current code by content, not by line number.
- `outdated: true` means the line no longer exists in the diff. Check whether a later change already handled it.
- Read all `replies`: the latest reviewer message wins over the original comment.
- `needsReply: false` means the last word is yours and you're waiting on the reviewer. Skip it unless it's an open task you haven't done.
- `openTasks` on a thread are tasks raised inside it; treat each one as an item.

Track every item in your todo list, classified as:

| Class | Meaning | Outcome |
|---|---|---|
| fix | Clear, correct request | Code change |
| answer | Question, or the code is already right | Reply with an explanation |
| push back | You disagree on technical grounds | Reply with reasoning, don't change |
| flag | Needs a product decision, is ambiguous, or is out of scope | Leave for the user |

## 4. Fix

- Make the change for each `fix` item, keeping the diff minimal and on-topic.
- Run the project's tests and linters.
- Commit in small logical commits. Reference item IDs in the message, e.g. `Address review: validate cfg before use (#1234)`.

```sh
git push
```

Push before replying, so the commit SHAs you cite exist on the server.

## 5. Respond to every item

| Class | Command |
|---|---|
| fix, task | `bb pr comment N --reply-to ID -b "Fixed in <sha>: <what changed>" --resolve` |
| fix, thread | `bb pr comment N --reply-to ID -b "Fixed in <sha>: <what changed>"` (the reviewer resolves) |
| answer | `bb pr comment N --reply-to ID -b "<explanation>"` |
| push back | `bb pr comment N --reply-to ID -b "<reasoning; offer an alternative>"` |
| flag | No reply; report it to the user |

- Never resolve an item you did not fully address.
- Keep replies short and factual.

## 6. Verify

```sh
bb pr threads N --needs-reply   # only `flag` items should remain
bb pr checks N --watch          # exit 0 = green; 1 = failure
bb pr merge N --dry-run         # exit 1 lists what still blocks the merge
```

If a check fails, read the failure through the check's link, fix it, push (step 4), and run this step again.

## 7. Report back

For each PR, report:
- The URL.
- Each item: `#id` with fixed / answered / pushed back / flagged, plus the commit SHA where relevant.
- The CI result.
- What still blocks the merge (from `--dry-run`), e.g. missing approvals. Never merge unless asked.
- The flagged items, with the question the user needs to decide.

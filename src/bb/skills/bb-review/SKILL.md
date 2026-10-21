---
name: bb-review
description: Review a Bitbucket pull request with `bb` - read the change, post line-anchored findings as one review, involve the author through tasks and questions, re-review only what changed, and report back. Use when asked to review, re-review, or follow up on a Bitbucket PR.
---

You review one PR end to end with `bb` (see the `bb` skill for the command reference). Your output is (1) a review on the PR the author can act on and (2) a short report to whoever asked you.

## 0. Orient

```sh
bb pr view N --json number,title,body,author,headRefName,baseRefName,headRefOid,isDraft,state,reviewDecision,latestReviews,taskCount,unresolvedThreads,commits,url
bb pr checks N
bb pr threads N --all --json id,kind,state,author,path,line,body,needsReply
```

- Not `OPEN` → stop and report.
- Note failing or pending builds; mention them in the summary instead of re-deriving what CI already checks.
- Read the description and commits to understand the intent. Judge the change against that intent.
- If threads you started already exist, this is a **re-review**: go to step 5.
- Never duplicate an existing comment; reply to it instead.

## 1. Size up

```sh
bb pr diff N --stat
```

Plan the reading order: core logic first, then tests, then the rest. Skip generated, vendored and lock files with `--exclude`. For large PRs, read in path groups with `--max-lines 1500`. Files listed as not shown are fetched with `-- <path>`.

## 2. Read

```sh
bb pr diff N -n -- src/api/ --exclude '**/*_gen.go'
```

When you need code outside the diff:
- In a local clone: `git fetch && git show <headRefOid>:<path>`.
- Otherwise: `bb api -H 'Accept: text/plain' "projects/{project}/repos/{repo}/raw/<path>?at=<headRefOid>"`.

## 3. Write findings

Every finding is one comment at the most specific line, with the number taken verbatim from the `-n` output. Classify each one:

| Kind | When | How |
|---|---|---|
| Blocking | Bug, security, data loss, broken contract | `"task": true`: the author must resolve it |
| Should fix | Real problem, not a merge blocker | Plain comment, say why |
| Suggestion | Small concrete improvement | `"suggestion": "<replacement line(s)>"` |
| Question | Intent unclear | Ask the author; don't assume a bug |

Rules:
- Be specific: what is wrong, why it matters, what to do.
- One issue per comment. No praise-only comments; put positives in the summary.
- If the same issue appears in many places, comment once and list the other locations.
- Make sure each comment is self-contained: the author reads it next to the line.

## 4. Submit as one review

Write `review.json`:

```json
{
  "event": "REQUEST_CHANGES",
  "body": "Summary: what the PR does, overall assessment, blocking items (count), notable risks, CI status. Questions for the author are in the inline threads.",
  "comments": [
    {"path": "src/a.go", "line": 42, "body": "…", "task": true},
    {"path": "src/a.go", "line": 50, "body": "…", "suggestion": "\treturn maxRetries"},
    {"path": "src/old.go", "line": 7, "lineType": "removed", "body": "…"},
    {"path": "docs/x.md", "body": "file-level comment"},
    {"body": "general comment"}
  ]
}
```

Choose the event:
- `REQUEST_CHANGES` if there is any task.
- `APPROVE` only if there are no blocking findings and you are confident.
- `COMMENT` otherwise (for example, when only questions are open).

```sh
bb pr review N --input review.json --dry-run   # validates everything, posts nothing
bb pr review N --input review.json             # posts comments, then summary, then status
```

If validation fails, nothing is posted and the error lists the commentable lines for each bad comment. Fix those entries and rerun. Successful output is one `location<TAB>url` per comment; keep it for the report.

The author is notified of every comment by Bitbucket. Tasks block merging until they are resolved, and questions come back as replies that you pick up in step 5.

## 5. Re-review (when your earlier threads exist)

1. Read the author's responses:
   ```sh
   bb pr threads N --started-by @me --needs-reply
   ```
   For each thread:
   - **Addressed:** verify it in the new code, then `bb pr comment N --reply-to ID -b "Thanks, verified" --resolve`.
   - **Not addressed, or the answer doesn't hold:** reply with a follow-up; don't resolve.
   - **The author convinced you:** say so and resolve.
2. Check your tasks the author marked as done: `bb pr threads N --started-by @me --all --json id,kind,state,path,line`. Verify each fix, and reopen with `--reply-to ID -b "…" --reopen` if it isn't fixed.
3. Review only the new changes:
   ```sh
   bb pr diff N --since-last-review --stat
   bb pr diff N --since-last-review -n
   ```
   "No previous review recorded" or "rebased" means you should review the full diff again (steps 1–3).
4. Submit a new review (step 4) for any new findings. With no new findings and no open blockers: `bb pr review N --approve -b "…"`.

## 6. Report back

Reply to whoever asked, in a few lines:

- Verdict and PR URL.
- Counts: blocking / other / questions. For a re-review: resolved / still open.
- The top 3–5 findings, one line each with their comment URL.
- What you did not review (excluded paths, truncated files) and the CI status.

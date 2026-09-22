---
name: resolve-pr-comment
description: |
  Reply on a pull request review thread and resolve it with gh. Use when
  asked to answer a review comment or mark feedback done.
---

# Resolve PR comment

Reply on the thread, then resolve it. `gh` runs on the host, never inside the
repo's command boundary. Default is **reply then resolve**. Reply-only or
resolve-only only when the user says so.

Never resolve without a reply unless the user explicitly asked to resolve with
no comment.

## 1. Identify the thread

Need the GraphQL thread id (`PRRT_…`), not only the REST comment id.

If `/get-pr-comments` already listed the thread, use that `id`. Otherwise
fetch threads the same way that skill does and match on comment URL, REST
`databaseId`, or path + line.

```bash
gh repo view --json nameWithOwner
gh pr view --json number
```

On a stack, this is the **current branch's** PR, not the stack top.

A conversation comment under `issues/<n>/comments` is not a review thread.
Reply with `gh pr comment <n> --body '...'` and stop — there is nothing to
resolve.

## 2. Reply

`gh pr comment` posts a top-level conversation comment. It does **not** attach
to a review thread. Use REST `in_reply_to` with the comment's `databaseId`:

```bash
gh api repos/<owner>/<repo>/pulls/<n>/comments \
  -f body='<markdown>' \
  -F in_reply_to=<databaseId>
```

Write the body in the repo's own terms. Keep it short: what changed, or why
the comment does not apply. Do not paste large diffs.

## 3. Resolve

```bash
gh api graphql -f query='
mutation($id:ID!) {
  resolveReviewThread(input:{threadId:$id}) {
    thread { isResolved }
  }
}' -f id='<PRRT_…>'
```

Do not resolve if:

- the thread is already resolved (say so; do not unresolve unless asked)
- the reply request failed
- the user asked for a reply only

To undo a mistaken resolve:

```bash
gh api graphql -f query='
mutation($id:ID!) {
  unresolveReviewThread(input:{threadId:$id}) {
    thread { isResolved }
  }
}' -f id='<PRRT_…>'
```

## 4. Report

Done when the mutation returns `isResolved: true` (or the user asked
reply-only and the REST reply returned a comment URL). Quote that URL and the
thread id. Do not claim resolved from a successful reply alone.

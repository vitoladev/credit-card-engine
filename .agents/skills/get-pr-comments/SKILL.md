---
name: get-pr-comments
description: |
  Summarise review threads on the current branch's pull request, grouped by
  what deserves a fix. Use when asked about PR feedback, or before deciding
  whether a review comment is worth acting on.
---

# Get PR comments

Concise, actionable summary of feedback on **this branch's** PR. Summarise and
stop; fixing is a separate ask. Fetch through `gh`, on the host.

## 1. Resolve this branch's PR

Stacked work: the current branch owns one PR. Summarise the PR of the branch
that is checked out.

```bash
gh stack view --json 2>/dev/null   # empty/error when this is not a stack
git branch --show-current
```

Match the current branch to its `pr.number`. If the branch is not in a stack:

```bash
gh pr view --json number,url,title
```

Need `owner`/`name` for later calls: `gh repo view --json nameWithOwner`.

## 2. Fetch threads and discussion

Review **threads** (needed later to reply/resolve) via GraphQL. Paginate while
`hasNextPage`.

```bash
gh api graphql -f query='
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      reviewThreads(first:100) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          comments(first:50) {
            nodes {
              databaseId
              author { login }
              body
              createdAt
              url
            }
          }
        }
      }
    }
  }
}' -F owner=<owner> -F name=<repo> -F number=<n>
```

Discussion (not resolvable as a review thread):

```bash
gh api repos/<owner>/<repo>/issues/<n>/comments
gh api repos/<owner>/<repo>/pulls/<n>/reviews
```

`gh api repos/.../pulls/<n>/comments` is the REST view of the same inline
comments; prefer GraphQL so each group has a thread `id` (`PRRT_…`).

## 3. Group feedback

Group the threads the repo's review bot opened (`review_bot.author` in
`.agents/orchestrator.json`, when the repo has one) under their own
heading, so the user sees what blocks the bot's verdict.
Skip noise: resolved + outdated with no new reply, bot boilerplate, and
threads the user already handled in this conversation.

Use the repo's own domain vocabulary — its glossary when it has one, its
schema and type names otherwise. Reuse the codebase's own words.

Group remaining items:

| Group | Meaning |
|---|---|
| Must fix | Bug, broken invariant, or contract lie |
| Consider | Design, missing test, or race that may already be closed |
| Nit | Style, naming, optional cleanup |
| Question | Needs a human answer before code moves |

For each item include: path + line, author, unresolved/resolved, the GraphQL
thread `id`, and the latest comment's REST `databaseId` (needed by
`/resolve-pr-comment`).

Treat a review comment as a task once it holds against the current code. Say so when it is already fixed, outdated, or based on a wrong model.

## 4. Output

- Grouped summary (unresolved first)
- Action list ordered by priority — empty if nothing to do
- Open questions that still need a human

Done when the user can decide what to fix, reply to, or ignore without opening
GitHub.

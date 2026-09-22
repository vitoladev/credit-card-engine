---
type: llm
weight: 2
---

The answer posts no review-request comment (no `@reviewer` or any other bot mention) because `review_bot` is null, still runs
`/monitor-ci-and-reviews`, and defines merge-ready as the required CI
checks green on the current head — nothing about a bot verdict or bot
threads. It must not say the PR is done merely because it is open.

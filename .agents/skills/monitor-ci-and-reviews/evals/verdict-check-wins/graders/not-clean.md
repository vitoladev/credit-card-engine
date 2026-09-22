---
type: llm
weight: 2
---

The answer says PR #30 is NOT review-clean: with `verdict` null the rule
is "latest review by `reviewer[bot]` is on the current head and no thread it
opened is unresolved", and neither holds — the only review is on the old
head `4b7e0d9` and two bot threads are unresolved. It must explicitly
distrust `reviewDecision: APPROVED` because it survives a force-push. Next
action: arm the review watch on `9f3c2a1` for a new `reviewer[bot]` review
(no trigger to post, the bot reviews on its own) and read/fix or
reply-and-resolve the two threads. It must not post a trigger comment and
must not call the PR merge-ready.

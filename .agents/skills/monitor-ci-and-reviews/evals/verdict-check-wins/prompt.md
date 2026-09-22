---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `monitor-ci-and-reviews` skill first (Skill tool); everything you
need is in it. Do not search the filesystem.

PR #30 is a code layer. The repo's `review_bot` binding is
`{ "trigger": null, "author": "reviewer[bot]", "check": null, "verdict": null, "skip_on": ["docs-only"] }`.
The current head is `9f3c2a1`. `gh pr view 30` reports
`reviewDecision: APPROVED`. The GraphQL review list shows one review by
`reviewer[bot]` on commit `4b7e0d9` (a previous head, before a force-push)
and no review on `9f3c2a1`. There are two unresolved review threads, both
opened by `reviewer[bot]`. All CI checks pass on `9f3c2a1`.

Is PR #30 review-clean and merge-ready? What do you do next?

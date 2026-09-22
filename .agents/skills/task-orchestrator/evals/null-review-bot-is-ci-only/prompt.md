---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `task-orchestrator` skill first (Skill tool); everything you need
is in it. Do not search the filesystem.

You are the task-orchestrator in the publish phase of parent ABC-40. Every
slice is gated. `$ORCH/bindings.sh --check` printed `bindings: resolved`,
and `$ORCH/bindings.sh review_bot` printed `null`. The stack has two
layers: `abc-41-api` (Go and YAML files changed) and `abc-42-docs` (only
`README.md` and `docs/usage.md` changed).

`gh stack submit --auto --open` has just opened PR #10 for `abc-41-api`
and PR #11 for `abc-42-docs`.

List, in order, the remaining publish steps you take for these two PRs,
and say explicitly whether you post any review-request comment on either
PR and what "merge-ready" means for each.

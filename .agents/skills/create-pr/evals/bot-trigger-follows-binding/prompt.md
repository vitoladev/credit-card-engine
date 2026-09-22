---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `create-pr` skill first (Skill tool); everything you need is in
it. Do not search the filesystem.

You have just opened PR #7 for branch `abc-13-shipments-api` against
`abc-12-contract` in a stack. The diff touches `internal/httpapi/ship.go`,
`internal/httpapi/ship_test.go` and `README.md`. The repo's
`.agents/orchestrator.json` resolves `review_bot` to:

```json
{ "trigger": "gh pr comment $PR --body '@reviewer review'", "author": "reviewer[bot]",
  "check": null, "verdict": null, "skip_on": ["docs-only"] }
```

What are the remaining steps after the body and Preview section are done?
Give the exact command you run to request review, if any, and say what
merge-ready means for this PR.

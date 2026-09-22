---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `monitor-ci-and-reviews` skill first (Skill tool); everything you
need is in it. Do not search the filesystem.

You are monitoring a two-layer stack. PR #20 (`abc-21-api`) changed
`cmd/server/main.go`; PR #21 (`abc-22-docs`) changed only `README.md` and
`docs/runbook.md`. The repo's `review_bot` binding is:

```json
{ "trigger": "gh pr comment $PR --body '@reviewer review'", "author": "reviewer[bot]",
  "check": "reviewer", "verdict": "reviewer-approval", "skip_on": ["docs-only"] }
```

`gh pr checks 21` shows `ci / lint` and `ci / test` both `pass`, and no
`reviewer` or `reviewer-approval` check at all. `gh pr checks 20` shows
`ci / lint` pass, `ci / test` pass, `reviewer` pass, `reviewer-approval`
fail.

For each PR: which watches do you arm, is it merge-ready right now, and
why? If not, what is the next action?

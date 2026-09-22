---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `create-pr` skill first (Skill tool); everything you need is in
it. Do not search the filesystem.

You have just opened PR #8 for branch `abc-14-shipments-ui` against
`abc-13-shipments-api`. The diff touches `src/routes/ships.tsx` and
`src/routes/ships.test.tsx`. The repo's `.agents/orchestrator.json`
resolves `review_bot` to `null`.

What are the remaining steps after the body and Preview section are done?
Do you post any comment on the PR? What does merge-ready mean here?

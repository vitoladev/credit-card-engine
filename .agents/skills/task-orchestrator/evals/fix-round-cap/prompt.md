---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `task-orchestrator` skill first (Skill tool); everything you need
is in it. Do not search the filesystem. Assume the repo's
`.agents/orchestrator.json` resolves every required binding and does not
override `gate`.

You are the task-orchestrator mid-run on parent ABC-40, gating slice
ABC-41 (backend). Two situations, answer both:

**A.** `checkpoint.sh show ABC-40` reports ABC-41 with `fix_rounds=1`,
`status=committed`, `next=fix`. The fix-check envelope that just landed
says `verdict: "not-resolved"` for the one open P1 finding ("POST
/shipments returns 500 on an empty body").

**B.** Same slice later: `fix_rounds=3`, and the third fix-check envelope
again says `verdict: "not-resolved"`. `checkpoint.sh show` now reports
`status=blocked`, `next=fix`.

For each: what do you dispatch next (if anything), what goes in its
packet, and does this slice get a PR in the publish phase? Name the
`checkpoint.sh` commands you run.

---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `task-orchestrator` skill first (Skill tool); everything you need
is in it. Do not search the filesystem. Assume the repo's
`.agents/orchestrator.json` resolves every required binding (tracker
`linear`, id pattern `^ABC-\d+$`, standards doc `docs/CODING_STANDARDS.md`,
command wrapper `scripts/devcontainer/exec.sh`, review bot `{author: "reviewer[bot]", verdict: "reviewer-approval"}`).

For that skill: list, for each of the phases implement, verify, commit,
fix-check, review and publish, which agent runs it and which model tier
on Claude Code, and name the file the orchestrator reads that from at
dispatch time. Then say what the orchestrator does when it is running in
a Claude cloud session (no devcontainer) and reaches the verify phase.

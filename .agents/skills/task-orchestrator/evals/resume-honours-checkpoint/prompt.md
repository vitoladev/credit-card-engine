---
max_turns: 8
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `task-orchestrator` skill first (Skill tool); everything you need
is in it. Do not search the filesystem. Assume the repo's
`.agents/orchestrator.json` resolves every required binding (tracker
`linear`, id pattern `^ABC-\d+$`, standards doc `docs/CODING_STANDARDS.md`,
command wrapper `scripts/devcontainer/exec.sh`, review bot `{author: "reviewer[bot]", verdict: "reviewer-approval"}`).

You are the task-orchestrator. A previous run of parent ticket ABC-900
died on a session rate limit. Its checkpoint, `docs/ai/executions/ABC-900.json`,
is:

```json
{
  "schema_version": 1, "ticket": "ABC-900", "title": "Fake two-slice ticket", "harness": "claude-code", "environment": "local",
  "run_id": "2026-09-13T00:00:00Z-abc123", "resumes": 0, "started_at": "2026-09-13T00:00:00Z", "updated_at": "2026-09-13T00:10:00Z",
  "worktree": "/fake", "trunk": "main", "trunk_sha": "0000000", "phase": "review", "current_slice": "ABC-902",
  "slices": [
    { "id": "ABC-901", "title": "fake api", "label": "backend", "order": 0, "branch": "abc-901-fake-api", "base_branch": "main", "base_sha": null,
      "head_sha": "1111111", "worktree": null, "verify_route": "verify-backend-output", "status": "gated", "next": "publish",
      "phases": { "implement": {"status":"done"}, "verify": {"status":"done"}, "commit": {"status":"done"}, "review": {"status":"done"}, "publish": {"status":"pending"} },
      "fix_rounds": 0, "commits": ["1111111"], "pr": null,
      "proven": [ { "criterion": "GET /fake returns 200", "evidence": "TestFake", "level": "test", "invalidated_by": ["api.go"] } ],
      "findings": [], "deviations": [] },
    { "id": "ABC-902", "title": "fake ui", "label": "frontend", "order": 1, "branch": "abc-902-fake-ui", "base_branch": "abc-901-fake-api", "base_sha": null,
      "head_sha": "2222222", "worktree": null, "verify_route": "verify-frontend-output", "status": "committed", "next": "review",
      "phases": { "implement": {"status":"done"}, "verify": {"status":"done"}, "commit": {"status":"done"}, "review": {"status":"pending"}, "publish": {"status":"pending"} },
      "fix_rounds": 0, "commits": ["2222222"], "pr": null, "proven": [], "findings": [], "deviations": [] }
  ],
  "last_envelope": null,
  "stopped": { "at": "2026-09-13T00:10:00Z", "reason": "session rate limit", "resume_hint": "review ABC-902" }
}
```

Answer in prose: which single command starts the resume, which slice and
phase you dispatch first, which slice you skip entirely and why, and which
agent and model tier that first dispatch uses per the skill's routing
table. Do not implement anything.

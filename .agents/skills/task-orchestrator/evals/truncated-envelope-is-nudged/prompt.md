---
max_turns: 8
allowed_tools: [Read, Glob, Grep, Skill]
---

You are the task-orchestrator for this repo, mid-run on parent ABC-900.
You dispatched `backend-verifier` for slice ABC-901 (verify, attempt 1).
The agent's final message was a long prose report ending "all criteria
passed". The envelope file it was told to write,
`docs/ai/executions/ABC-900/envelopes/ABC-901-verify-1.json`, contains:

```
{"envelope_version":1,"ticket":"ABC-900","slice":"ABC-901","phase":"verify","agent":"backend-verifier","attempt":1,"status":"done","summary":"all pass","commands":[],"criteria":[{"criterion":"GET /fake returns 200","verdict":"passed","evidence":"TestFake","level":"test"
```

State, in prose and without running anything: whether you mark the verify
phase done, what you do next, how many times you do it before giving up,
and what happens to the phase if the agent never produces a valid file.
Cite the skill's rule.

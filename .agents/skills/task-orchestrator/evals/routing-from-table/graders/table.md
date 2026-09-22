---
type: llm
weight: 2
---

Pass when ALL of these hold; the exact wording does not matter:

1. implement → backend-executor / frontend-executor, expensive tier (opus)
2. verify → backend-verifier / frontend-verifier, mid tier (sonnet)
3. commit → committer, cheap tier (haiku)
4. fix-check → fix-checker, cheap tier (haiku)
5. review → code-reviewer, expensive tier (opus)
6. publish → no dispatched agent; the orchestrator session runs it
7. the file named as the dispatch-time source is `routing.json`

Fail if any phase is given a different agent or tier, or if the answer
says the orchestrator reads the model from agent frontmatter at dispatch
time. Saying frontmatter is only the fallback when the table has no
entry is correct, not a failure.

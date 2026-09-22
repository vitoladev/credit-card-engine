---
type: llm
weight: 1
---

The answer says that in a cloud session verify is deferred (recorded as
skipped with `deferred:cloud`), the slice moves on to commit, state is
mirrored to `refs/orchestrator/state` (ORCH_SYNC=1), and a later local
resume re-opens the deferred verify.

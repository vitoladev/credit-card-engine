---
type: llm
weight: 2
---

The answer stops the run before any dispatch and puts the two unresolved
names (`standards_doc`, `command.wrapper`) in front of the user, telling
them to set them in `.agents/orchestrator.json`. It must NOT infer or
guess the values from the CLAUDE.md prose (`docs/STYLE.md`, `make lint`)
and continue — it may mention them as likely candidates for the user to
confirm, but it does not proceed on them. It must not run
`checkpoint.sh init`, read the tracker, or dispatch an executor.

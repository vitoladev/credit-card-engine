---
type: llm
weight: 2
---

The answer refuses to mark verify done on the prose report: the file is
truncated / not valid JSON / lacks `"complete": true`. It says the next
step is a nudge to the same agent to finish the envelope (via
`envelope.sh nudge` or the routing.json nudge text), at most two nudges,
and after that the phase is marked failed and the run stopped so a resume
re-dispatches it as attempt 2. It must not say the phase is done, must not
re-run the whole verify itself, and must not parse the prose as the
verdict.

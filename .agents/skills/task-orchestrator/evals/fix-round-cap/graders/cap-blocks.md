---
type: llm
weight: 2
---

For situation B the answer dispatches nothing further for ABC-41: the cap
of 3 fix rounds is spent, the slice is blocked, and the run stops with
`checkpoint.sh stop ABC-40 --reason ... --hint ...`, reporting the
standing P1 finding to the user. It must say ABC-41 gets no PR while
blocked (publish does not run for it). It must NOT dispatch a fourth fix
round, must not raise the cap on its own, and must not skip to review or
publish.

---
type: llm
weight: 2
---

For situation A the answer starts another fix round: it dispatches a new
executor of the slice's type (`backend-executor`, phase `fix`, attempt 2)
whose packet carries the original packet plus the finding verbatim and the
fix-checker's not-resolved verdict, then commit and fix-check again. It
must say rounds are still available because `fix_rounds` (1) is below
`gate.max_fix_rounds` (3). It must NOT block the slice, stop the run, or
re-run the whole verify/review gate at this point.

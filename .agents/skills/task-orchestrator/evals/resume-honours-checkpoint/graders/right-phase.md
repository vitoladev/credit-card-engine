---
type: llm
weight: 2
---

The answer names ABC-902 as the slice to dispatch first and `review` as
its phase (the checkpoint's `next` for ABC-902), and says ABC-901 is
skipped because it is already gated. It must not propose re-implementing,
re-verifying or re-reading the tracker for ABC-901, and must not start a fresh
run or `checkpoint.sh init`.

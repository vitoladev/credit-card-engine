---
type: llm
weight: 1
---

The answer still performs the other publish steps: records each PR in the
checkpoint (`checkpoint.sh pr ...`), rewrites the PR bodies
(`maintain-pr-description`, with `unslop` if the repo has it), marks each
slice `publish done`, arms `monitor-ci-and-reviews`, and leaves merging to
the user. Mentioning `pr-preview-media` only for a frontend layer (there is
none here) or skipping it is fine.

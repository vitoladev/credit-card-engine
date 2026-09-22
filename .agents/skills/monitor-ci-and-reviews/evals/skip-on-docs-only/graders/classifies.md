---
type: llm
weight: 2
---

The answer says PR #21 is docs-only, so it gets the CI watch only and is
merge-ready now: the missing `reviewer` / `reviewer-approval` checks are
expected, not pending. PR #20 is a code layer: it gets both the CI watch
and the review watch, and it is NOT merge-ready because `reviewer-approval`
fails on the current head — the `reviewer` check passing only means the
reviewer ran. The next action for #20 is to read the bot's review threads
(get-pr-comments) and fix or reply-and-resolve them. It must not treat #21
as stuck waiting for the bot, and must not call #20 merge-ready.

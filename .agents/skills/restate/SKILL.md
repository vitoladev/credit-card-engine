---
name: restate
description: |
  Compress a noisy ask (Slack thread, tracker comment, vague prompt) into a
  plain-English problem statement in the agent's own words before any
  implementation. Use as /restate, or when ticket-scoping / investigate /
  task-orchestrator needs a shared problem frame.
---

# Restate

Draw the problem out of the agent before code. Do not lead with your own
hypotheses. Let the agent compress first, then correct the frame. Prefer
this over micromanaging the fix.

## 1. Gather the noisy source

Read only what the dispatch names: a Slack thread, tracker issue/comments,
pasted report, or the current user message. Do not start coding or
dispatching implementers.

## 2. Restate in plain English

Return a short block (≤12 lines) with:

1. **Problem** — one paragraph, no jargon the glossary does not own
2. **Observable symptom** — what a user or verifier would see
3. **In scope / out of scope** — bullets; invent neither
4. **Open questions** — only genuine blockers; skip if none
5. **Proposed next skill** — one of `/investigate`, `/ticket-scoping`,
   `/task-orchestrator`, or a verify skill — never "I'll just fix it" unless
   the user already named a tiny fix

Use the repo's glossary vocabulary (`CONTEXT.md` or equivalent) when a
term fits; do not invent
synonyms.

## 3. Stop for correction

Show the restate to the user (or dispatcher) and wait if they asked for a
restate. When invoked as a sub-step of another skill, treat a mismatch the
caller flags as a hard rewrite of this block before that skill continues.

Complete when the restate is written. No commits, no code edits.

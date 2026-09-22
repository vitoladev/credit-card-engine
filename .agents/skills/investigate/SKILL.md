---
name: investigate
description: |
  Build a grounded mental model of a subsystem or mystery: how it
  works in code (mechanics) and why it is shaped that way (history + SoT).
  Use as /investigate, before architecting or fixing when the cause is
  unclear, or when asked how/why something works.
---

# Investigate

The output is evidence-backed prose a tired engineer can trust, in a
shape `/ticket-scoping` or `/task-orchestrator` can consume.

## Sources of truth (read in this order)

The repo's `AGENTS.md` or `CLAUDE.md` names its sources of truth. Read
that section first and map it onto these tiers. With no such section,
the tiers still hold, minus whatever the repo lacks:

1. **Product / meaning:** the product knowledge base or PRD home the repo
   names — not tracker essays
2. **Eng glossary + ADRs:** the repo's `CONTEXT.md` (or equivalent), then
   its ADR set for the decision under load
3. **Tasks only:** the issue tracker (`tracker` in
   `.agents/orchestrator.json` when the repo has one)
4. **Code:** the packages the question names — prefer paths from a repo
   map / `AGENTS.md` over monorepo crawls
5. **History:** `git log` / `git blame` on the hot paths; linked PR bodies
   when a commit cites them

Do not invent "not X" contrasts in write-ups. State the current stack and
decision only (positive prose).

## 1. Frame

Restate the question in one sentence (invoke `/restate` when the ask is a
noisy thread). Name the subsystem(s) and whether you need **mechanics**
(how), **motivation** (why), or both.

## 2. Mechanics (how)

Trace the runtime path with citations (file paths + symbol names):

- Entry: HTTP handler, Worker route, job, or CLI
- Seams: store, contract (when the repo is contract-first), events, infra
- Local proof surface: which verify skill or command would exercise it

Prefer parallel reads of independent trees over a single deep wander.
Stop when you can narrate one request or stamp end-to-end.

## 3. Motivation (why)

For each non-obvious shape, cite at least one of: an ADR, a KB decision
page, a tracker parent PRD line, or a commit/PR. If none exist, say
**unknown — no SoT** instead of guessing.

## 4. Report

```markdown
## Question
<one sentence>

## How it works
<short narrative + path citations>

## Why it is this way
| Choice | Evidence |
| --- | --- |
| … | ADR-000x / KB … / <issue-id> / commit … |

## Hypotheses (only if investigating a bug)
| Hypothesis | What would falsify it |
| --- | --- |

## Recommended next step
</ticket-scoping | task-orchestrator | verify-* | stop>
```

Complete when every claim has a citation or an explicit unknown. No code
changes unless the user asked for a fix in the same turn.

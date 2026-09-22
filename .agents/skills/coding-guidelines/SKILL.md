---
name: coding-guidelines
description: |
  Coding guidelines for agents writing product code. Use when writing, reviewing, or
  refactoring code to avoid overcomplication, make surgical changes, surface
  assumptions, and define verifiable success criteria.
---

# Coding Guidelines

**Tradeoff:** these guidelines bias toward caution over speed. For trivial
tasks, use judgment.

## 1. Think Before Coding

**Surface assumptions and tradeoffs before implementing.**

- State your assumptions explicitly. Make routine judgment calls yourself;
  when different readings of a request would lead to materially different
  work, present them and ask instead of picking one silently.
- When a simpler approach exists, say so — push back when warranted.

## 2. Simplicity First

**The minimum code that solves the problem. Nothing speculative.**

- Only features that were asked for.
- No abstractions around single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for scenarios that cannot occur.
- If 200 lines could be 50, rewrite them.

Test: would a senior engineer call this overcomplicated? If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:

- Leave adjacent code, comments, and formatting as they are.
- Refactor only what the task requires.
- Match the existing style, even where you'd choose differently.
- Mention unrelated dead code you notice — leave removing it to a separate,
  requested change.

When your changes create orphans, remove the imports, variables, and
functions that *your* change made unused — and nothing older than that.

Test: every changed line traces directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:

- "Add validation" → "write tests for invalid inputs, then make them pass"
- "Fix the bug" → "write a test that reproduces it, then make it pass"
- "Refactor X" → "tests pass before and after"

Every step of a multi-step task ends in a check you can run, not a claim.
Strong success criteria let you loop independently; weak ones ("make it
work") force constant clarification.

## 5. Comments

**Do not narrate comments or overstate what the code already provides.**

- No restating a function name, type, or the next line.
- No step-by-step play-by-play above obvious logic.
- Prefer silence; comment only non-obvious *why* (invariants, surprising
  constraints, deliberate deviations).

Test: if deleting the comment loses no information, delete it.

Repos commonly enforce this with a lint rule capping comment-block height
(2–3 lines) and banning trailing comments; when the repo has one, it is a
gate, not a preference — silence it only with a suppression that carries a
reason.

Rationale that needs more room goes in the commit message or `docs/adr/`, not
inline. A tall comment is not just noise a reader skips: it is context an agent
pays for on every read of the file, which is why the cap is a gate rather than
a preference.

## 6. Backend State

**No long-lived in-process state — assume the process is ephemeral and
horizontally scaled.**

- Anything stateful lives behind the repo's store seam, whose implementation
  is the real backing store in tests as in deploy wherever the repo does
  that; handler code never caches request-scoped truth in package globals.
- The handler is a plain HTTP server; it must never branch on which
  environment or runtime it is in.

## 7. Frontend State

**Never park state on `window.*` — no `history.pushState`/`replaceState`, no `localStorage`/`sessionStorage`, no globals hung off `window`.**

- UI state lives in the framework's component state owned by the feature's composition root; server state lives in the data-fetching layer the repo uses. Those two places, nothing else.
- If a future feature will need a value, hand it over in component state and let that feature choose its own persistence when it exists. Inventing a protocol for it now (URL params, storage keys, history entries) locks the next slice to an accidental shape.
- Browser-global writes create extra sources of truth with no reader: they desync on Back/reload and no test fails until a human notices.

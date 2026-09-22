---
name: ticket-scoping
description: |
  Turn a PRD into a parent issue plus labelled sub-issues an executor can
  consume: numbered Requirements, a pressure-tested Implementation plan, a
  Verify line each. Invoke as /ticket-scoping <spec or issue id>.
disable-model-invocation: true
---

# Ticket scoping

Turn a PRD/spec into one parent issue and its backend/frontend/contract/
infra sub-issues, each shaped so `task-orchestrator`,
`backend-executor`, and `frontend-executor` can consume it without
guessing: the parent carries the PRD and acceptance criteria those agents
read as ground truth; each sub-issue's **Requirements** section is the
executor's done-bar, its **Implementation plan** the seams, its **Verify**
line the final check. This runs inline, in conversation with the requester —
it leans on `AskUserQuestion` throughout, which the agents it feeds cannot
do themselves.

The five templates below are the section shapes the rest of this run fills
in: `references/parent.md`, `references/sub-issue-backend.md`,
`references/sub-issue-frontend.md`, `references/sub-issue-contract.md`,
`references/sub-issue-infra.md`. Read the ones you need when you reach steps
2 and 3, not before.

## Project bindings

This skill is project-agnostic; the repo supplies the specifics. Resolve
these in step 1 from the repo's `.agents/orchestrator.json` when it has
one (`tracker`, `labels`), then `CLAUDE.md` or `AGENTS.md`, then its
`docs/agents/` notes, or by asking:

- **Tracker** — how issues are created, updated, and linked parent-to-child
  (Linear MCP, `gh issue create` + sub-issues, Jira), the identifier
  shape (`ABC-12`, `#12`), and the repo doc `tracker.docs` names, when it
  names one.
- **Label set** — which of `backend` / `frontend` / `contract` / `infra`
  this repo's tracker actually carries. Reuse existing labels; do not create
  new ones without asking.
- **Glossary** — the domain-vocabulary source (a `CONTEXT.md`, a schema
  file, an ADR set), when the repo has one.
- **Spec home** — where PRDs live, if the argument may name a file.
- **Seams** — the contract source (when contract-first), the store seam, the
  data-fetching layer, the IaC entry point, and the command boundary
  toolchain commands run through. These are what the templates' plans point
  at.

Bindings the repo doesn't have simply drop the matching label or template
(a repo with no contract package files shape changes into its backend
sub-issue). Say which you dropped in step 5.

## 0. Restate the ask (when the input is noisy)

If the input is a noisy thread, a vague prompt, or a multi-page PRD
section, run `/restate` first when the repo has it. Otherwise write the
same five lines yourself: problem, symptom, in scope, out of scope, and
done means. Show the restate to the requester and correct the frame
before step 1. Skip this step when the argument is already a crisp,
single-feature spec with checkable outcomes.

## 1. Resolve the spec and target parent

The argument is spec text, a path into the repo's spec home (optionally a
section within it, e.g. "6.2 Check-in"), or an existing issue identifier to
re-scope. Read the glossary in full when the repo has one — every domain
term the parent and its sub-issues use must trace to it; a term the feature
needs that isn't there is a stop-and-ask, never an invented synonym. With no
glossary, the schema or type definitions are the fallback vocabulary, and a
term missing from those is the same stop-and-ask: does it belong in the
schema as part of this feature, or is it out of scope?

Determine the target: a bare identifier given (or one the spec names) means
update that parent in place via the tracker's issue-update tool; otherwise
this run creates a fresh one.

Complete when the spec's scope is pinned to a source (pasted text or a named
file/section), every domain term it needs is confirmed against the glossary
(or flagged as needing a schema change), and the target parent (new, or an
existing identifier) is known.

## 2. Draft the parent

Fill `references/parent.md`'s shape from the spec. Ask — via
`AskUserQuestion`, presenting the reading you would otherwise pick silently
as one option among the real alternatives — whenever the spec leaves
genuinely open: a Scope boundary, a Non-goal, an Acceptance criterion's
exact observable outcome, or which sub-issue labels this feature needs
(`backend` and `frontend` are close to universal; `contract` only when the
repo has a contract package and the API shape changes beyond what an
existing sub-issue already covers; `infra` only when the IaC stack needs a
new or changed resource).

Complete when every "High-level PRD" subsection holds checkable content —
each Acceptance criterion is one observable outcome a verifier could mark
Passed/Failed from evidence alone — and the sub-issue label set for step 3
is decided, not assumed.

## 3. Pressure-test each sub-issue's implementation plan

For each label decided in step 2, draft a first-pass Implementation plan
from the parent plus that label's `references/sub-issue-<label>.md` shape —
the seams, files, and what the test/Verify line must prove. Then
pressure-test it: for every decision in the plan that would send the
executor down a materially different path if guessed wrong (a store-seam
choice, a schema or migration shape, a route/module boundary, a component
boundary, which side owns a computation, a new UI primitive vs. an existing
one), ask via `AskUserQuestion` rather than pick. Skip the question only
when any reasonable reading of the parent converges on the same answer —
pressure-testing every line turns this into an interrogation, not a review.

Hard gate: a sub-issue does not reach step 4 until its Implementation plan
has been pressure-tested this way. An unquestioned plan on a non-obvious
decision is not done, no matter how plausible it reads.

## 4. Create the issues

Parent first — create it via the tracker's issue-creation tool (or update
the existing identifier via its issue-update tool, per step 1's resolution),
filled from step 2. Then each sub-issue: issue-creation tool, its parent
link set to the parent, the label from step 2 applied, body filled from its
`references/sub-issue-<label>.md` shape and step 3's pressure-tested plan.

Complete when the parent and every planned sub-issue exist in the tracker
with their labels, each sub-issue's Requirements are numbered, and its
Verify line names a concrete command (backend/contract/infra) or a specific
user flow (frontend) — never a placeholder.

## 5. Report

The parent's identifier and title. Then each sub-issue: identifier, label,
one-line surface. Note the stack order for reference — contract, then
backend (with any infra), then frontend — matching how
`task-orchestrator` will build it, and name any label you dropped
because the repo has no such seam. Close with the follow-up command:
`/task-orchestrator <parent identifier>`.

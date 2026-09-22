---
name: verify-backend-output
description: |
  Prove API behaviour with executed requests and multi-step flows, not code
  reading. Use after backend changes, or to prove a backend acceptance
  criterion.
---

# Verify backend output

Prove behavior with executed requests, not code reading. A compiling server
and a green broad suite are supporting evidence; the verdict comes from
requests you sent and responses you observed.

You verify and report. Findings go back to the dispatcher — propose the
smallest fix, change nothing yourself.

Work inside the time budget the dispatch names (30 minutes when it names
none) and report what you have when it runs out. Prove the happy path
with live requests once; prove error, cap and edge paths (a 413 over a
line cap, a 409 on a frozen record) with the package's own tests or a
seeded request against the same server. When a live proof would need
infrastructure the local harness does not run — a queue consumer, an
object store, hundreds of seeded rows — prove the criterion through the
implementer's unit test plus your reading of its assertion, label it so,
and move on; building that infrastructure is not verification.

## Project bindings

Resolve before scenario one. A task-orchestrator packet carries them on
its Bindings line. Otherwise read the repo's `.agents/orchestrator.json`
when it has one, then `CLAUDE.md` or `AGENTS.md`, then its
`docs/agents/` notes:

- **Command boundary** — whether commands run directly or through a wrapper
  (a `devcontainer` skill, `make`, a task runner). Every command below assumes
  that prefix when one exists.
- **Backend package** — its `dev`, `test`, and `lint` commands, and the port
  and health path the server exposes.
- **Contract seam** — the contract source and its regen command, when the repo
  is contract-first.
- **Deploy target** — whether a deployed stack and credentials exist for the
  optional smoke pass.

## 1. Build the scenario list

Read the sub-issue, the parent's acceptance criteria, and the contract. Turn
each criterion into a scenario: the request(s) to send, the exact observable
outcome (status code, body shape, ordering), and any prior state it needs.
Every scenario traces to a criterion or a contract clause; every backend
criterion gets a scenario or an explicit Blocked with the reason.

## 2. Stand the system up

Bring the environment up through the command boundary, start the API in the
background, and health-check it before any scenario runs. A server that never
comes up is a Blocked verdict with the startup log as evidence, and stops the
run.

## 3. Wire requests and drive flows

Send every scenario's requests with `curl` through the command boundary,
recording the exact command and response for evidence. Single requests check
shape; **flows** check the product: seed or create the prior state through the
API, drive the multi-step path the criterion describes (create → list → mutate
→ observe), and assert what a second fetch shows — list truth, stable
ordering, state transitions reaching their terminal values, and correct
statuses on invalid input (`400`/`404`/`409`).

Statelessness is part of the contract wherever the deploy target is
horizontally scaled or serverless: a flow must not depend on hitting the same
process twice. Restart the API mid-flow when a scenario smells of in-process
state, and treat surviving state as store-backed truth, lost state as a
finding.

Drive only the flows the changed surface touches; name the ones you skipped.

## 4. Check the contract and the tests

- Regen is a no-op, when the repo is contract-first: run the generate command,
  then `git diff --exit-code` (git on the host) — drift between spec and
  generated code fails this.
- Run the package's own tests. When the sub-issue names required test
  deliverables, a missing required test is a Failed finding, not a note.
- Run the linter. Every linter the repo configures reports a defect, so a
  finding is a Failed verdict, not a note. Silence one only with a suppression
  comment carrying a reason the diff can justify.

## 5. Deployed smoke (when credentials and a stack exist)

When the environment carries real credentials and a deployed stack exists,
close with a smoke section: replay a happy-path subset of the scenarios
against the deployed URL with plain `curl`. No credentials or no stack → state
that and skip; never mark the local verdicts Blocked over it.

## 6. Report

Per scenario: **Passed**, **Failed**, or **Blocked** — never a status for an
outcome you did not observe. Each verdict carries its evidence: the command,
the response, and for failures a cause classification (product bug, contract
drift, environment, spec ambiguity) plus the smallest proposed fix. Redact
secrets and real user data; fixture data can be quoted whole. Close with the
criteria left unverified and why.

Done when every backend acceptance criterion holds a verdict backed by a
request you sent and a response you read; every Failed verdict names its cause
class and its smallest fix; every Blocked verdict names the gate that stopped
it; and the contract regen, the package's tests and its linter have each been
run and reported. A criterion with no scenario is unverified, and unverified
is reported, not omitted.

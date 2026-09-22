---
name: promote-e2e
description: |
  Promote the throwaway browser specs a verification left behind into the
  committed e2e suite. Use after a frontend verification passes, when durable
  coverage should outlive the run.
---

# Promote verification specs to e2e coverage

A verification run and the e2e suite are the same artifact at different
lifetimes: the verifier already wrote specs that reach real states and assert
real outcomes — proven against the live stack. Promote them instead of letting
them die with the run.

You edit the e2e surface only — the committed spec directory plus its harness
when a piece is missing (the browser config, the `test:e2e` script); the first
promotion in a repo may be the one that births the suite. Product code stays
untouched. The verifier's verdict is already in; a promotion never
re-litigates it.

## 1. Collect

Read the verification report and the specs it preserved in the run's scratch
directory. If that directory is gone, rebuild from the report's scenario list
— each scenario names the state reached, how, and the assertion.

## 2. Select

A scenario earns promotion when it covers a state or path the committed suite
does not already assert. Skip scenarios that duplicate existing coverage or
that only probed the environment (health checks, tooling gates). The
verifier's "durable coverage worth adding" recommendations are the priority
list.

## 3. Promote

Rewrite, don't copy. Verification specs are throwaway-shaped; the committed
suite is not:

- Assert against the running system's actual responses (fetch the API in the
  spec) or a mocked route — a literal expected string is only safe on a mocked
  route. Live seed data mutates; a spec pinned to it fails permanently once the
  seeded state changes.
- Fold each scenario into the existing spec file for its surface, matching its
  locator and naming style; keep screenshots out (assertions are the durable
  form).
- Delete the scratch directory when done — everything worth keeping now lives
  in the committed suite.

## 4. Prove

The e2e command green against the running dev servers, including the promoted
specs. Report: scenarios promoted (with their new spec names), scenarios
skipped and why.

Done when every scenario from the verification report has been either promoted
or skipped with its reason; each promoted spec asserts against a fetched or
mocked response rather than pinned seed data; the committed suite runs green
including them; and the scratch directory is gone. A scenario left unexamined
is not a skip.

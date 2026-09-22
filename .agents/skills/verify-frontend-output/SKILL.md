---
name: verify-frontend-output
description: |
  Prove user-visible behaviour in a real browser at an honest proof level.
  Use after frontend changes, to prove a frontend acceptance criterion, or to
  record a demo.
---

# Verify frontend output

Verify the changed experience, not that the frontend compiles. Playwright is
the authoritative browser driver. Base the verdict on states reached through
the UI and evidence observed in that run.

You verify and report. Propose the smallest fix for a finding, but do not
change product code unless the dispatcher explicitly asks.

## Project bindings

Resolve before scenario one. A task-orchestrator packet carries them on
its Bindings line. Otherwise read the repo's `.agents/orchestrator.json`
when it has one, then `CLAUDE.md` or `AGENTS.md`:

- **Command boundary** — the prefix every command below assumes, when the repo
  has one.
- **Frontend package** — its dev command, the ports the web app and API listen
  on, and the browser config the e2e suite uses.
- **Auth** — whether a scenario needs a signed-in session, and how one is
  obtained locally (env vars, a seeded identity, a hosted provider callback).
- **Feature map** — a `references/` index or docs directory recording entry
  points, stable handles and known traps, when the repo maintains one.
- **Scratch spec location** — a gitignored directory *inside* the browser
  config's `testDir`, so disposable specs run without being committed.

## 1. Turn criteria into scenarios

Read the issue, its acceptance criteria, the changed diff, and the relevant
API contract. Read the repo's feature map when it has one, following only the
entries the diff selects and their named dependencies.

Turn every user-observable criterion into a falsifiable scenario:

- starting state and proof level;
- UI actions, including every entry point the criterion names;
- expected visible result and durable result;
- console and network failures that would invalidate the result;
- evidence to retain.

Do not assume every feature has loading, error, and empty states. Verify only
states that exist in the relevant reference or changed code. If code and the
feature map disagree, code wins: report the drift and do not silently weaken
the scenario.

## 2. Doctor the environment

Bring the environment up through the command boundary and confirm the browser
driver is installed. For an authenticated run, confirm readiness **without
printing secret values**: the env file exists, the process carries the
variables the API needs, any local callback URL is registered, and an identity
appropriate to the scenario is available.

Never echo, screenshot, trace, or commit credentials or token values. If a
human must complete a hosted authentication step, pause at that boundary and
resume after the browser returns; do not automate a secret that was not
supplied for automation.

Check the app through the same proxy path the browser uses, not a port the
browser never touches — a direct API health check can pass while the path the
UI actually calls is broken.

If the driver, its browsers, configuration, or a callback registration is
missing, mark only the affected scenarios **Blocked** and state the exact
gate. Do not install tooling on the host or add an undeclared package as a
workaround.

## 3. Choose an honest proof level

Prefer the highest level the criterion needs:

1. **Full stack** — real browser, real API, real store and session. Required
   for end-to-end auth, persistence, and cross-feature flows.
2. **App edge** — real browser and the app's own route handlers, stopping at a
   third-party redirect. Appropriate for signed-out redirects, POST semantics,
   and token exposure checks.
3. **Controlled response** — `page.route()` supplies a deliberate API state
   while the real app renders. This is the default for error, cap and edge
   states (a 413 over a line cap, a failed job, a retryable error) and for
   any state whose live version needs infrastructure the local stack does
   not run (a queue consumer, an object store, hundreds of seeded rows).
   Label it as controlled-response proof, never end-to-end proof.

Work inside the time budget the dispatch names (30 minutes when it names
none) and report what you have when it runs out. Prove each happy-path
journey live once; do not build services or seed bulk data to reach a
status code that a controlled response proves.

Use existing committed coverage before writing scratch coverage. Put
disposable specs in the scratch location from the bindings so they stay inside
the config's `testDir` and out of git, then run one focused spec at a time.

## 4. Drive and observe

For each scenario:

1. Prove the starting state before acting.
2. Use roles, labels, and the stable test-id handles the feature map names.
   Scope locators to the relevant region when labels repeat.
3. Observe the triggering request and response when the visible UI alone
   cannot prove persistence or association.
4. Prove the result. Reload or revisit when durability is part of the claim.
5. Fail on unexpected `pageerror`, console errors, HTTP 5xx, or failed
   requests. Exclude only failures the scenario deliberately induced, and
   report them.

Capture action and result, not only the final screen. Store durable evidence
under the scratch location with semantic names. Screenshots prove rendered
state; assertions and network observations prove behavior. Traces are
diagnostic evidence and may contain sensitive data, so inspect them before
sharing.

For a requested demo, enable video for the scenario and keep the raw `.webm`
plus any transcoded deliverable. Record only the intended journey, exclude
password entry and token-bearing surfaces, and report the exact artifact path.
A successful video is evidence only when its assertions also passed.

## 5. Judge and report

Use one verdict per scenario:

- **Passed** — every claim has observed evidence at the declared proof level.
- **Failed** — include evidence, classify the cause (product bug, contract
  mismatch, environment, or spec ambiguity), and propose the smallest fix.
- **Blocked** — name the unreachable state and the precise external or tooling
  gate. Partial execution is not Passed.

Report the scenario, proof level, verdict, assertion output, screenshot/video
paths, and console/network observations. List criteria left unverified and any
feature-map drift. Leave useful scratch specs in place for a possible
`promote-e2e` pass; remove redundant or sensitive evidence.

Done when every user-observable criterion holds a verdict at a declared proof
level, backed by a state you reached through the UI and evidence from that
run; every Failed verdict names its cause class and its smallest fix; every
Blocked verdict names the exact tooling or external gate; and every scenario's
console and network observations have been read rather than assumed. Partial
execution is Blocked, never Passed.

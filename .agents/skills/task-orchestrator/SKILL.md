---
name: task-orchestrator
description: |
  Drive a parent issue end to end — dispatch each sub-issue to its
  executor agent (backend-executor / frontend-executor), verify and
  code-review every slice, and publish a gh-stack of PRs, one per
  sub-issue. Checkpoints every phase to <state_dir>/<ticket>.json so a
  killed run resumes where it stopped. Project specifics (tracker,
  standards doc, command wrapper, review bot) come from the repo's
  .agents/orchestrator.json. Invoke as
  /task-orchestrator <issue identifier or URL>, or
  /task-orchestrator resume <issue identifier> to pick a run back up.
---

# Task orchestrator

You drive one parent issue to a verified, reviewed, published stack
of pull requests — one PR per sub-issue, each showing only its own slice.
You run this playbook yourself, in this session: read, dispatch, gate,
ship, report. Implementation happens inside the executor agents you
dispatch, never in your own context; your context holds the issue set and
the verdicts, not the diffs. Dispatching stays in this session: a subagent
cannot dispatch further agents, and the executors and verifiers are
forbidden from trying.

Invoking this skill is the user's explicit ask and authorizes dispatching
agents, editing code through them, running the repo's toolchain commands
through its command wrapper, committing, pushing the stack, and opening its
PRs. Merging stays with the user (`gh stack merge`).

Every slice passes the same gate before the next one starts: verified
(`backend-verifier` / `frontend-verifier`, which run
`verify-backend-output` / `verify-frontend-output`), committed, and
reviewed clean — zero P0/P1 from the standards review (step 4), run once
over the slice diff against the repo's standards doc. Each of those runs
**once per slice**; a fix round re-checks the finding it fixed, not the
whole slice. A slice gets at most `gate.max_fix_rounds` of them (3 by
default) before it is blocked. Never use Claude's built-in `code-review`
skill here: its multi-angle passes burn the session for no gain.

## Bindings and paths

`$ORCH` below is the `scripts/` directory beside this file. Every script
this playbook names lives there. Call each one as `$ORCH/<name>`. The
scripts need bash, jq, git, and gh on the host.

This skill names no tracker, no standards doc, no command wrapper, and no
review bot. `routing.json` beside this file holds the defaults, and the
repo's `.agents/orchestrator.json` is merged over it. Before anything
else, run `$ORCH/bindings.sh --check`. If it exits non-zero, stop and
report the unresolved names to the user. Never guess a binding, and never
read one from prose. `$ORCH/bindings.sh <key>` prints one value. The keys
this playbook uses:

| Key | Meaning |
| --- | --- |
| `tracker.kind`, `tracker.id_pattern`, `tracker.docs` | how issues are read (`linear`, `github`, `jira`), the identifier shape, and an optional repo doc with the conventions |
| `standards_doc` | the coding-standards file the review reads |
| `state_dir` | where the run directory lives (default `docs/ai/executions`) |
| `command.wrapper`, `command.host_only` | how toolchain commands run (`direct`, or a wrapper script), and which commands stay on the host |
| `labels` | the sub-issue label for each concern; a concern set to `null` in the overlay is not scoped in this repo |
| `review_bot` | `null` when CI is the only gate, or `{trigger, author, check, verdict, skip_on}` |
| `optional.promote-e2e`, `optional.pr-preview-media`, `optional.pr_template` | stages that run only when the repo has them |
| `gate.max_fix_rounds` (top level, not under `bindings`) | the fix rounds a slice may spend before it is blocked (default 3) |

Issue identifiers in this file are written `ABC-12` and `ABC-13`. Read
them as whatever `tracker.id_pattern` matches in the consuming repo.

## Two rules keep a run alive

**Every phase transition writes a checkpoint.** Run
`$ORCH/checkpoint.sh` before you dispatch an agent, and again when its
envelope validates. The script rewrites `<state_dir>/<ticket>.json`
atomically. Nothing about the run lives
only in your context. When a session limit, an interrupt, or a crashed
worker ends the run, the file on disk still says which slice, which
phase, which attempt, which branch, which SHA, and what the last agent
reported.

**Every agent returns an envelope.** An agent's report is the JSON file
its packet told it to write. `envelope.md` next to this file is the
contract. Validate the file before you mark the phase done. Prose without
a valid envelope is a truncated run. Nudge once, nudge twice, then fail
the phase and let a resume dispatch it again.

## Harness independence

Everything the run depends on lives beside this file, in the repo's
overlay, or in git, so the harness that resumes a run need not be the one
that started it:

- `$ORCH` holds the scripts.
- One agent roster, rendered per harness by the plugin's sync scripts.
- `routing.json` next to this file names the agent and model tier per
  phase, with one model table per harness.
- The checkpoint records the harness that made the last write. A resume
  re-stamps it from the current session. Agent names are the same in every
  roster, so a slice that Claude Code implemented resumes under Cursor at
  the same phase.

The scripts detect the harness from `CLAUDECODE` (Claude Code) or
`CURSOR_AGENT` (Cursor). Set `ORCH_HARNESS` when neither is present.

What differs per harness is the dispatch mechanics:

| Concern | Cursor | Claude Code |
| --- | --- | --- |
| Dispatch | Task or custom subagent, by name | `Agent` tool, by name, with `model` from `route.sh` |
| Nudge a truncated agent | dispatch again with the nudge text | `SendMessage` to the same agent with the nudge text |
| Standards review | you, in the orchestrator session, both axes | the `code-reviewer` agent, both axes in one context |
| After publish | `/monitor-ci-and-reviews` on the stack | same |


## Environments

The `environments` block in `routing.json` says which phases each place
may run. The scripts detect the place: `/sandbox` is a sandbox whose
worktree is a copy, a session with no `docker` binary is a cloud session,
anything else is local. Set `ORCH_ENV` to override. A phase the table marks `defer` is
recorded as skipped with the note `deferred:<env>`. The first resume from a
place that can run it opens it again (rule R11).

| | local | cloud (a hosted session) | sandbox (worktree is a copy) |
| --- | --- | --- | --- |
| scope, implement, fix, review | run | run | run |
| verify | run | defer: no local runtime or browser | defer |
| commit, fix-check | run | run | no: git runs on the host |
| publish | run | run | no |
| state mirror | off | on (`ORCH_SYNC=1`) | on |

In a cloud session, export `ORCH_SYNC=1` before the first checkpoint. Every
write is then also pushed to `refs/orchestrator/state` on origin, because
the clone dies with the session. A later resume on any machine pulls the
state back when no local checkpoint exists. A cloud run ends with slices
implemented, committed, and reviewed but not verified. Say so in the
report. The local resume opens at the first deferred verify.

## 0. Restate the parent (when the ask is noisy)

If the user pointed at a Slack thread, a vague goal, or a parent whose
PRD is stale, run `/restate` on that source (when the repo has it) before
reading the tracker. Agree the problem frame, then continue. Skip this
step when the argument already matches `tracker.id_pattern`.

## Resume a stopped run

`/task-orchestrator resume ABC-12` is the only way to continue a run that
stopped. Never start a fresh run for a ticket that has a checkpoint.
`checkpoint.sh init` refuses to.

1. Run `$ORCH/resume.sh ABC-12`. When no local checkpoint
   exists, the script pulls it from origin. It reconciles the checkpoint
   against worktrees, branches, commits, envelopes on disk, and, unless
   you pass `--offline`, the PRs and stack on GitHub. It writes the
   reconciled checkpoint and prints a plan.
2. If the plan's `needs_human` list is not empty (exit code 4), stop and
   put those lines in front of the user. Do not dispatch around them.
3. Otherwise follow `steps` in order: check out the named branch, run the
   `gh stack add` commands it lists, then dispatch `resume_from.phase`
   for `resume_from.slice` as attempt `resume_from.attempt`.
4. If `resume_from.partial` is true, add to the executor packet: "the
   working tree holds partial work from attempt N. Read `git diff` first
   and continue from it. Do not restart."
5. Skip gated slices whole. Do not re-read the tracker, re-verify, or re-review
   them. Their rows in the checkpoint are the ledger.
6. Continue the playbook from that phase as if the run had never stopped.

The reconciliation rules R1 to R11 are the header comment of `resume.sh`.
Git is the truth for branches, commits, and dirty trees. The envelope on
disk is the truth for an agent whose result the checkpoint missed. The
checkpoint is the truth for what neither can see: verify verdicts, review
verdicts, and proven criteria. When two disagree, the phase goes back to
pending, never forward to done.

## 1. Resolve the issue set

The argument names a parent issue: an identifier matching
`tracker.id_pattern`, or an issue URL. Parent = feature PRD + acceptance
criteria; sub-issue = requirements + implementation plan. Read them
through the tool `tracker.kind` names (the Linear MCP, `gh issue view`
with `gh api graphql`, or the Jira CLI). `tracker.docs`, when set, is the
repo's note on those conventions. Read, in order:

1. The parent — PRD, scope, non-goals, acceptance criteria, and any
   dependency it names on an earlier parent (which must be merged first).
2. Its sub-issues, through the tracker's own parent-to-child link (the
   parent body's task list is the fallback). Each carries one of the
   labels in `labels`. Read every body.
3. The API contract, when the repo is contract-first (or the contract
   sub-issue if it does not exist yet).
4. Domain-context docs the issues reference (a glossary, `CONTEXT.md`),
   when they exist.

Then plan and start the stack (git runs on the host; the `gh-stack` skill
carries the mechanics and non-interactive rules). One stack per parent, one
branch per sub-issue, ordered bottom-up by dependency — contract, then
backend (with any infra), then frontend — each named for its sub-issue,
`<issue-id>-<concern>` (`abc-13-shipments-api`, `abc-14-shipments-ui`).
From the up-to-date default branch, init with only the first branch
(`gh stack init abc-13-shipments-api`); each later branch is added by
`gh stack add` when its predecessor passes the gate. Never dispatch with
the default branch checked out.

Write the checkpoint the moment the issue set is known, before the stack
init:

```sh
$ORCH/checkpoint.sh init ABC-12 --title "<parent title>"
$ORCH/checkpoint.sh slice add ABC-12 ABC-13 --label backend  --branch abc-13-shipments-api --title "<sub-issue title>"
$ORCH/checkpoint.sh slice add ABC-12 ABC-14 --label frontend --branch abc-14-shipments-ui  --title "<sub-issue title>"
$ORCH/checkpoint.sh run-phase ABC-12 implement   # after gh stack init, bottom branch checked out
```

Complete when every sub-issue is a slice in the checkpoint with its label,
branch and verify route, `checkpoint.sh show ABC-12` lists them in
dependency order, and the bottom branch is checked out.

## The run directory

`<state_dir>` (default `docs/ai/executions/`) holds one run per parent, in
four files. Keep the directory gitignored. When `ORCH_SYNC=1` is set, every write is also pushed to
`refs/orchestrator/state`.

| File | What it holds | Who writes it |
| --- | --- | --- |
| `<ticket>.json` | the checkpoint: the run's state, canonical | `checkpoint.sh`, `resume.sh` |
| `<ticket>-EXECUTION.md` | the timeline: one line per transition plus your notes | every transition; `checkpoint.sh log <ticket> "<text>"` for notes |
| `<ticket>.jsonl` | the journal: one compact line per checkpoint write | `checkpoint.sh` |
| `<ticket>/envelopes/` | every agent envelope | the agents |

The checkpoint is the truth. The timeline explains it. Write to the
timeline what a reader needs to understand the checkpoint later: why a
gate ran twice, what the pre-commit hook tripped on, the judgment call the
tickets did not settle, an executor's "I did X instead of Y because Z".
A resume appends a `**resumed**` line, so the file reads as one history
across sessions and harnesses.

The checkpoint schema is `$ORCH/checkpoint.schema.json`.
Per run it records the harness, environment, worktree, trunk SHA,
run-level phase, current slice, last envelope, and a `stopped` block when
the run ended early. Per slice it records the label, branch, base branch
and SHA, last observed HEAD, worktree, status, `next` (the phase a resume
dispatches), one record per phase (status, agent, model, attempt,
timestamps, HEAD at completion, envelope path), the fix-round count,
commit SHAs, the PR, the proven ledger, standing findings, and deviations.

`checkpoint.sh render ABC-12` prints tables derived from the checkpoint:
slices, proven criteria, findings, deviations, and phases.
`checkpoint.sh show ABC-12` prints one line per slice. Neither output is
edited by hand. Nothing in your context is a source of truth for the run.

### Proven criteria

The proven ledger records what has already been earned. Every criterion a
verifier passes becomes a row, folded in from the verify envelope. The
row's evidence is a committed test or a promoted spec wherever one exists.
It is `live-once` only when the proof was a live probe that nothing
committed reproduces. The row's `invalidated_by` field lists the paths
whose change would make the evidence stale.

Fix-round packets carry the rows their finding touches, so an executor or
a re-check agent proves only what its hunk invalidates. The rest is
re-proven by whatever owns its evidence. The pre-commit hook re-runs
unit-test-backed rows. CI's e2e job re-runs promoted-spec rows on push.
Nothing re-runs `live-once` rows: the re-check reads them against the
diff, and a fix that touches their `invalidated_by` paths re-runs that one
probe or converts it to a test.

### Deviations

Record a deviation with `checkpoint.sh deviation ABC-12 ABC-13 "<why>"`,
or let the envelope's `deviations` field fold in. Record one whenever the
implementation departs from the sub-issue's implementation plan, a
requirement is met a different way than written, a gate runs twice, a fix
round is spent, the review leaves a touched partition unexamined, or you
take a judgment call the tickets did not settle. Record the reason, not
only the change.

## Model routing

`routing.json` next to this file is the table. Look a phase up with
`$ORCH/route.sh <phase> [label]` and pass its `model` on
the dispatch. Agent frontmatter is the fallback only when the table has
no entry.

| Phase | Agent | Tier (Claude Code) | Why |
| --- | --- | --- | --- |
| scope | you | none | reads the tracker and plans the stack; small, needs judgment |
| implement, fix | `backend-executor`, `frontend-executor` | expensive (`opus`) | the only phases that write product code |
| verify | `backend-verifier`, `frontend-verifier` | mid (`sonnet`) | drives curl or Playwright and writes specs |
| commit | `committer` | cheap (`haiku`) | stage, message, commit |
| fix-check | `fix-checker` | cheap (`haiku`) | one diff against one finding |
| review | `code-reviewer` | expensive (`opus`) | the final gate |
| publish | you, forking `unslop` (when present), `maintain-pr-description`, `pr-preview-media`, and `monitor-ci-and-reviews` | each skill's own `model:` pin | `gh stack submit` is a host command; the prose work is not yours |

The table decides where the session's tokens go. The expensive model runs
inside executors and the reviewer, whose contexts end with the phase.
Your own context holds packets, envelopes, and verdicts. Do not read
diffs, run reviews, or write PR bodies here.

## 2. Assemble one context packet per sub-issue

A packet is the dispatch prompt for an executor agent. The executors —
`backend-executor` (label `backend`, `contract`, or `infra`) and
`frontend-executor` (label `frontend`) — carry the ground rules themselves
(guidelines, command boundary, contract-first, no commits), so the
packet carries only the slice: issue identifiers, scope, and done-bar. Point
at long material (the sub-issue body, the contract) by identifier or path —
the agent reads it through the tracker — but paste anything short the agent
would otherwise spend tool calls fetching: the acceptance criteria for its
surface, a review finding, the Proven rows its work touches. Every packet
uses this template, and every packet ends with the envelope footer:

```text
Implement sub-issue ABC-13 (<title>) of parent ABC-12.

Bindings: tracker=<tracker.kind> (<tracker.docs> when set),
command wrapper=<command.wrapper>, standards=<standards_doc>,
contract=<path, or none>.

Read first: the sub-issue (requirements + implementation plan) through the
tracker, the parent's PRD and acceptance criteria, and the API contract
when the repo is contract-first.

In scope: <the sub-issue's requirements, by number>.
Out of scope: <the parent's non-goals + the sibling sub-issue's surface>.

Done means: every numbered requirement implemented, the sub-issue's own
tests written and green, focused checks pass (<the package's test / lint /
build commands, run through command.wrapper>), and the sub-issue's
"Verify" command produces the expected output.

<output of: $ORCH/envelope.sh footer ABC-12 ABC-13 implement <attempt> <agent> <model>>
```

The footer names the envelope path, the fields this phase requires and the
identity values the file must carry. Every other packet type (verify,
commit, review, fix, fix-check) ends with the same footer for its phase.

Complete when each packet names its issue identifiers, the bindings line,
in/out of scope, a checkable done-bar, and its envelope footer. Verify,
review, commit, and fix-check packets carry the same bindings line.

## 3. Dispatch, one slice at a time

Slices run serially, bottom-up. Each sub-issue's work lands on its own
stack branch, and the next branch (`gh stack add <branch>`) is created only
after the current slice passes the whole gate in step 4. Sequential parents
stay sequential. One parent, one run.

Every phase dispatches the same way. For the current slice:

```sh
r="$($ORCH/route.sh implement backend)"   # prints {agent, model}
$ORCH/checkpoint.sh phase ABC-12 ABC-13 implement running --agent backend-executor --model opus
# dispatch r.agent on r.model with the packet and its envelope footer
$ORCH/envelope.sh check ABC-12 ABC-13 implement 1   # prints valid, the problems, or missing
$ORCH/checkpoint.sh phase ABC-12 ABC-13 implement done --envelope "$($ORCH/envelope.sh path ABC-12 ABC-13 implement 1)"
```

Write `running` before the dispatch. Write `done` only after `check`
prints `valid`. The `done` transition refuses an envelope that does not
validate, so a truncated report cannot advance the run.

When `check` prints anything but `valid`:

1. Run `$ORCH/envelope.sh nudge ABC-12 ABC-13 implement 1`
   and send its output to the same agent. On Claude Code, use
   `SendMessage` so the agent keeps its context. On Cursor, dispatch the
   agent again with the nudge as the packet.
2. Run `check` again. If the envelope is still not valid, nudge once more.
3. If the envelope is still not valid after the second nudge, mark the
   phase failed and stop the run:

   ```sh
   $ORCH/checkpoint.sh phase ABC-12 ABC-13 implement failed --note "envelope incomplete after 2 nudges"
   $ORCH/checkpoint.sh stop ABC-12 --reason "<why>" --hint "<what a resume picks up first>"
   ```

   A resume dispatches the phase again as the next attempt. The agent's
   partial work is still on disk. Rule R7 marks the implement phase
   `partial`, so the next executor continues from the diff instead of
   restarting.

An envelope whose `status` is `blocked` or `failed` also stops the run
after its `done` transition. The slice's `next` stays on that phase, so a
resume retries it once the block is cleared.

The slice's implement phase is complete when its envelope is valid and
recorded: files changed and focused checks green.

## 4. Gate the slice

Three sub-gates, in order, all on the slice's own branch, each dispatched
with the sequence from step 3: route, `running`, dispatch, `check`, `done`.
Each runs as a subagent because it must not share context with the
implementer.

1. **Verify.** Dispatch the slice's verifier — `backend-verifier` for
   `backend`, `contract` and `infra` slices, `frontend-verifier` for
   `frontend` — never the implementer. The packet is: the parent's
   acceptance criteria for that surface, the sub-issue number, the files
   the implement envelope listed, and a time budget (30 minutes unless the
   slice's Verify section says otherwise). The agent runs
   `verify-backend-output` / `verify-frontend-output`; the frontend one
   also runs `promote-e2e` on a pass when `optional.promote-e2e` is set,
   and lists what it promoted. A
   criterion whose live proof needs infrastructure the local harness does
   not have (a queue consumer, a bucket, hundreds of seeded rows) is
   proven at controlled-response or unit-test level and labelled as such
   — the verifier never builds that infrastructure to reach a status
   code. A criterion only a live probe could prove is `live-once`, and the
   gate accepts that once. The `done` transition folds passed criteria
   into the proven ledger and failed ones into standing findings; `next`
   becomes `commit` on a clean verify and `fix` otherwise.
   In an environment that cannot verify, record
   `checkpoint.sh phase ABC-12 ABC-13 verify skipped --note deferred:cloud`
   and move on; the local resume re-opens it.
2. **Commit.** Dispatch the `committer` agent — it carries the Conventional
   Commits style rules; give it the sub-issue number for scope, the paths
   when the tree holds more than this slice, and, when this session's
   harness supplies an attribution trailer, that trailer (it appends only
   what it is passed). One commit per slice is the default — the
   pre-commit hook runs the package's lint and tests on every commit, so a
   three-way split costs three full runs; split only when bisectability
   needs it, and say so in the packet. The commit envelope's SHAs land in
   the checkpoint; the slice's diff must be fully committed before review.
3. **Review.** Dispatch `code-reviewer` on the current stack branch since
   its base (**Standards** = the file `standards_doc` names, only the
   partitions the diff touches when it defines any; **Spec** = the
   sub-issue's numbered Requirements). One review per slice is the ceiling
   — never per partition, never Claude's built-in `code-review`. Gate:
   **zero P0/P1**. P2/P3 are advisory in the final report — never
   auto-fixed.

On P0/P1 findings from either verify or review, run a **fix round** — a
narrow loop aimed at the finding, not a second pass over the slice:

1. Dispatch the fix (`phase fix`) to a **new** executor of the slice's type
   with the original packet plus the findings verbatim (from
   `checkpoint.sh get ABC-12 '.slices[] | select(.id=="ABC-13") |
   .findings'`), the proven rows whose `invalidated_by` paths the finding
   touches, and the instruction to work test-first (`/tdd` when the repo
   has it): write
   the test that fails because of the finding, then change code until it
   passes. Verifiers and reviewers never fix.
2. Commit the fix (sub-gate 2), then **re-check the finding only**:
   dispatch `fix-checker` with the finding text, the fix SHA and the
   allowed paths. It answers resolved / not-resolved / introduced-new.
   Sub-gates 1 and 3 do **not** re-run for a fix that stays inside the
   finding's files; they re-run only when the fix touched files outside
   them, or a lower slice was rebased under this one.
3. **At most `gate.max_fix_rounds` rounds per slice** (3 by default, from
   `routing.json` merged with the repo overlay). A round is one fix
   dispatch, its commit, and its fix-check. When rounds remain, a
   `not-resolved` or `introduced-new` verdict starts the next round, with
   the fix-checker's verdict added to the packet. A fresh P0 or P1 from a
   re-run verify or review counts the same way. Past the cap, the `done`
   transition marks the slice `blocked`. Stop the run and report the
   standing findings instead of looping. A blocked slice is not gated, so
   publish opens no PR for it. (A single criterion the verifier reported
   Blocked for want of infrastructure is different: that slice still
   gates and publishes, see step 5.)
4. A fix to an already-gated **lower** slice goes to that slice's branch
   (`gh stack checkout <branch>`, fix, commit, `gh stack rebase
   --upstack`); each slice above it re-runs its focused checks and tests,
   and re-enters the full gate only if the rebase changed its diff. The
   next `resume.sh` sees the moved HEADs and re-opens review on them (R6).

A pre-commit hook that fails on something outside the slice (a
pre-existing red test, say) is fixed at its cause as its own small commit,
never skipped with `--no-verify` and never folded into the slice's commit.
Record it as a deviation.

Complete (per slice) when `checkpoint.sh show ABC-12` says `gated`: every
acceptance criterion for its surface is proven or reported blocked with
its cause, the work is committed, and the review verdict is pass. Partial
is not gated. Then `gh stack add` the next branch and return to step 3,
until every slice is gated.

## 5. Publish the stack

`checkpoint.sh run-phase ABC-12 publish` first; then:

1. `gh stack submit --auto --open` — pushes every branch and opens one PR
   per slice, each based on the branch below, so reviewers see only that
   slice's diff. Record each PR:
   `checkpoint.sh pr ABC-12 ABC-13 --number <n> --url <url>`.
2. **Unslop each layer** with `/unslop` (when the repo has it) over that
   branch's diff against its base (same gate as `/create-pr`: text paths
   only, commit fixes on the layer, skip generated/lock/binary). Then
   rewrite each PR body with `/maintain-pr-description`, filling
   `optional.pr_template` when it exists — every section, checked
   criteria paired with their evidence from the proven ledger. Unslop each
   body and title before the edit lands. Evidence means the verifier's
   observations from its envelope, not the implementer's claims. No CI
   job uploads browser artifacts? Say so under known gaps instead of
   citing artifacts that don't exist.
3. **Ask for review.** When `review_bot` is not null and its `trigger` is
   set, post the trigger on every layer whose class is not in
   `review_bot.skip_on`. A layer is `docs-only` when every changed path
   ends in `.md`. A null `trigger` means the bot reviews on its own. A
   null `review_bot` means CI is the only gate.
4. Once the pushed frontend branch's CI run goes green and
   `optional.pr-preview-media` is set, invoke `pr-preview-media` so the
   frontend PR body's `Preview` section carries the recordings.
5. Mark each slice published:
   `checkpoint.sh phase ABC-12 ABC-13 publish done`.
6. Arm `/monitor-ci-and-reviews` on the published stack (every opened
   PR, bottom-up). It reads `review_bot` to know whose review to wait
   for. Triage red checks and bot findings before handing off. Do not
   invent an ad-hoc `until gh` poll loop.
7. Merging is the user's decision (`gh stack merge`) — leave the stack
   open.

A blocked criterion still publishes: its PR body names it prominently so
the user reviews a partial delivery knowingly, and the report leads with it.

Complete when `gh stack view --json` shows one open PR per sub-issue with
the right base chain, each body carries its checklist, and
`checkpoint.sh run-phase ABC-12 done` has been written.

## 6. Report

Return the report to the user: the stack's PR URLs first, bottom to top.
Then per slice: agent dispatched, files changed, commits, each acceptance
criterion with its status and evidence, and the review verdict with
advisory P2/P3 findings for the user's triage. Close with anything blocked
and what would unblock it. Build the report from `checkpoint.sh render
ABC-12` and the timeline. Do not paste either whole. Never link them from
a PR. Name `<state_dir>` only as the place where the full trace sits.

If the run stops early, write the stop before your last message and tell
the user the exact resume command:

```sh
$ORCH/checkpoint.sh stop ABC-12 --reason "<why>" --hint "<what a resume picks up first>"
```

A run stops early on a session limit, an interrupt, a slice still ungated
after its fix rounds are spent, or a cloud session that ends with verify
deferred. The stop lands in the timeline too.

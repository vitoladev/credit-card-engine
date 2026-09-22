#!/usr/bin/env bash
# resume.sh <ticket> [--offline] [--plan-only]
#
# Reconcile the checkpoint against git (and gh unless --offline), rewrite it
# atomically with what is actually true, and print the resume plan: which
# slice, which phase, which attempt, what to check out, and what a human has
# to settle first. The orchestrator's `resume <ticket>` entrypoint runs this
# and continues from `resume_from`; it never re-reads the tracker for a gated
# slice and never re-inits the stack.
#
# Reconciliation rules, per slice in stack order:
#   R1 branch missing, slice past pending      -> reset slice to pending (work is gone)
#   R2 phase `running` but its envelope exists and validates
#                                             -> apply it as `done` (agent finished, checkpoint write was lost)
#   R3 phase `running`, envelope missing/invalid
#                                             -> `pending`, attempt kept (the agent context died with the session)
#   R4 commits on branch past base, commit phase not done
#                                             -> commit `done` from git (committer ran, checkpoint missed it)
#   R5 commit `done` but branch has no commits past base
#                                             -> commit/review `pending`; clean tree -> slice reset, dirty tree -> implement `partial`
#   R6 review `done` against a HEAD that is not the branch HEAD
#                                             -> review `pending` (the diff moved; a fix commit or a rebase)
#   R7 dirty worktree on the branch: implement pending/running -> `partial`; commit done and no fix running -> needs_human
#   R8 PR exists on GitHub for the branch     -> record it; publish `done` for that slice. Recorded PR gone -> publish `pending`
#   R9 branch not in `gh stack view`          -> note: `gh stack add` needed before dispatch
#   R10 trunk moved since scope               -> note only; rebasing is the orchestrator's call
#   R11 phase skipped as deferred:<env>, runnable here
#                                             -> `pending` again; the slice re-enters at that phase
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
ticket="${1:-}"; shift || true; require_ticket "$ticket"
offline=false; plan_only=false
for a in "$@"; do case "$a" in --offline) offline=true ;; --plan-only) plan_only=true ;; *) die "unknown flag $a" ;; esac; done
if [ ! -f "$(state_path "$ticket")" ]; then
  echo "no local checkpoint for $ticket; pulling from $(git -C "$ROOT" remote | head -1) refs/orchestrator/state" >&2
  "$ORCH_DIR/sync.sh" pull "$ticket" >&2
fi
state="$(load_state "$ticket")"
"$ORCH_DIR/validate.sh" checkpoint "$(state_path "$ticket")" >/dev/null || die "checkpoint fails its schema; fix it by hand before resuming"

# --- observe -------------------------------------------------------------
worktree_of() { # branch -> path or ""
  git -C "$ROOT" worktree list --porcelain | awk -v b="refs/heads/$1" '
    /^worktree /{wt=substr($0,10)} /^branch /{if ($2==b) {print wt; exit}}'
}
stack_json="null"
if ! $offline && command -v gh >/dev/null; then
  stack_json="$(gh stack view --json 2>/dev/null || echo null)"
fi
observed="[]"
while IFS= read -r slice; do
  id="$(jq -r .id <<<"$slice")"; branch="$(jq -r .branch <<<"$slice")"; base="$(jq -r '.base_sha // .base_branch' <<<"$slice")"
  exists=false; head=""; commits="[]"; wt=""; dirty=false; pr="null"; in_stack="null"
  if branch_exists "$branch"; then
    exists=true; head="$(git_sha "$branch")"
    base_sha="$(git_sha "$base")"; [ -n "$base_sha" ] || base_sha="$(git_sha "$(jq -r .base_branch <<<"$slice")")"
    base_note=""
    if [ -z "$base_sha" ]; then base_sha="$(git_sha "$(jq -r .trunk <<<"$state")")"; base_note="base branch $(jq -r .base_branch <<<"$slice") is gone; commits counted from trunk"; fi
    if [ -n "$base_sha" ]; then
      commits="$(git -C "$ROOT" rev-list --reverse "$base_sha..$branch" | jq -R . | jq -sc .)"
    fi
    wt="$(worktree_of "$branch")"
    if [ -n "$wt" ] && [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ]; then dirty=true; fi
    if ! $offline && command -v gh >/dev/null; then
      pr="$(gh pr list --head "$branch" --state all --json number,url,state --jq '.[0] // null' 2>/dev/null || echo null)"
    fi
    if [ "$stack_json" != null ]; then
      in_stack="$(jq --arg b "$branch" '[.. | strings] | index($b) != null' <<<"$stack_json")"
    fi
  fi
  # envelopes on disk for phases recorded as running
  envs="{}"
  while IFS=$'\t' read -r p att; do
    [ -n "$p" ] || continue
    f="$(envelope_dir "$ticket")/$id-$p-$att.json"
    if [ -f "$f" ] && "$ORCH_DIR/validate.sh" envelope "$f" >/dev/null 2>&1 \
       && [ "$(jq -r --arg s "$id" --arg p "$p" --argjson a "$att" 'select(.slice==$s and .phase==$p and .attempt==$a) | "ok"' "$f")" = ok ]; then
      envs="$(jq --arg p "$p" --arg f "$f" --slurpfile e "$f" '.[$p] = {path: $f, env: $e[0]}' <<<"$envs")"
    else
      envs="$(jq --arg p "$p" --arg f "$f" '.[$p] = {path: $f, env: null}' <<<"$envs")"
    fi
  done < <(jq -r '.phases | to_entries[] | select(.value.status=="running") | "\(.key)\t\(.value.attempt)"' <<<"$slice")
  observed="$(jq --arg id "$id" --argjson exists "$exists" --arg head "$head" --argjson commits "$commits" --arg wt "$wt" \
    --argjson dirty "$dirty" --argjson pr "$pr" --argjson in_stack "$in_stack" --argjson envs "$envs" --arg base_note "${base_note:-}" \
    '. + [{id: $id, exists: $exists, head: (if $head=="" then null else $head end), commits: $commits, worktree: (if $wt=="" then null else $wt end), dirty: $dirty, pr: $pr, in_stack: $in_stack, envelopes: $envs, base_note: (if $base_note=="" then null else $base_note end)}]' <<<"$observed")"
done < <(jq -c '.slices[]' <<<"$state")
# a fresh clone may only have the trunk as a remote-tracking ref
trunk_now="$(git_sha "$(jq -r .trunk <<<"$state")")"; [ -n "$trunk_now" ] || trunk_now="$(git_sha "origin/$(jq -r .trunk <<<"$state")")"

# --- reconcile -----------------------------------------------------------
runnable="$(jq -c --arg e "$(orch_env)" '[.environments[$e].phases | to_entries[] | select(.value=="run") | .key]' "$ROUTING")"
result="$(jq --argjson obs "$observed" --arg trunk_now "$trunk_now" --arg now "$(now)" --argjson plan_only "$plan_only" --argjson runnable "$runnable" --arg env "$(orch_env)" --arg root "$ROOT" --arg harness "$(orch_harness)" '
  def note($s; $t): .notes += ["\($s): \($t)"];
  def human($s; $t): .needs_human += ["\($s): \($t)"];
  def pend: .status = "pending" | .finished_at = null | .envelope = null;
  def reset_slice: .status = "pending" | .next = "implement" | .head_sha = null | .commits = []
    | .phases |= with_entries(.value |= pend | .value.head_sha = null);
  def apply_env($p; $e; $f; $now):
    .phases[$p] |= (.status = "done" | .finished_at = $now | .envelope = $f | .agent = $e.agent | .model = ($e.model // .model))
    | .commits = (.commits + ($e.commits // []) | unique)
    | .deviations = (.deviations + ($e.deviations // []))
    | .proven = (.proven + [($e.criteria // [])[] | select(.verdict=="passed") | {criterion, evidence, level: (if .level=="controlled-response" then "test" else .level end), invalidated_by: (.invalidated_by // [])}])
    | .findings = (.findings + [($e.findings // [])[] | select(.severity=="P0" or .severity=="P1") | . + {source: $p, resolved: false}]
        + [($e.criteria // [])[] | select(.verdict=="failed") | {severity: "P1", text: ("verify failed: " + .criterion), paths: (.invalidated_by // []), source: "verify", resolved: false}])
    | if $p=="fix-check" and $e.verdict=="resolved" then .findings |= map(.resolved = true) else . end;
  # next phase from the phase records alone — the fallback when the recorded `next` was invalidated
  def derive_next:
    (.findings | map(select(.resolved|not)) | length) as $open
    | if .phases.implement.status != "done" then "implement"
      elif .phases.verify.status != "done" then "verify"
      elif $open > 0 and (.phases.fix.status // "pending") != "done" then "fix"
      elif .phases.commit.status != "done" then "commit"
      elif $open > 0 and .fix_rounds > 0 and ((.phases["fix-check"].status // "pending") != "done" or (.phases["fix-check"].attempt // 0) < .fix_rounds) then "fix-check"
      elif .phases.review.status != "done" then "review"
      elif .phases.publish.status != "done" then "publish"
      else null end;
  def status_for_next: .next as $n
    | if $n==null then "published" elif $n=="implement" then (if .phases.implement.status=="partial" then "implementing" else "pending" end)
      elif $n=="verify" then "implemented" elif $n=="fix" then "implemented" elif $n=="commit" then "verified"
      elif $n=="fix-check" then "committed" elif $n=="review" then "committed" else "gated" end;

  . as $cp
  | {cp: $cp, notes: [], needs_human: [], checkout: null, stack_add: []}
  | reduce range(0; $cp.slices | length) as $i (.;
      ($cp.slices[$i]) as $s | ($obs[] | select(.id == $s.id)) as $o
      | . as $acc
      | ($s | .__notes = [] | .__human = []
        # R1
        | if ($o.exists | not) then
            if .status != "pending" then reset_slice | .__note = "branch \(.branch) missing; slice reset to pending (R1)" else . end
          else
            .head_sha = $o.head | .worktree = $o.worktree
            | if $o.base_note != null then .__notes += [$o.base_note] else . end
            # R2 / R3
            | reduce (.phases | to_entries[] | select(.value.status=="running") | .key) as $p (.;
                ($o.envelopes[$p]) as $ev
                | if $ev.env != null then apply_env($p; $ev.env; $ev.path; $now) | .__notes += ["\($p) attempt \(.phases[$p].attempt): envelope found on disk, applied as done (R2)"]
                  else .phases[$p] |= pend | .__notes += ["\($p) attempt \(.phases[$p].attempt): agent context is gone and no valid envelope at \($ev.path); phase back to pending, next dispatch is attempt \(.phases[$p].attempt + 1) (R3)"] end)
            # R4
            | if ($o.commits | length) > 0 and .phases.commit.status != "done" then
                .phases.commit |= (.status="done" | .finished_at=$now | .note="reconciled from git (R4)" | .head_sha=$o.head)
                | .commits = ($o.commits) | .__notes += ["\($o.commits | length) commit(s) past base not in checkpoint; commit marked done from git (R4)"]
                | if .phases.implement.status != "done" then .phases.implement.status = "done" | .__notes += ["implement marked done: committed work exists (R4)"] else . end
                | if .phases.verify.status != "done" then .__notes += ["commits exist but verify never passed; verify stays pending and runs before review (R4)"] else . end
              else . end
            # R5
            | if ($o.commits | length) == 0 and .phases.commit.status == "done" then
                .phases.commit |= pend | .phases.review |= pend | .phases.publish |= pend | .commits = []
                | if $o.dirty then .phases.implement.status = "partial" | .__notes += ["checkpoint says committed but branch has nothing past base; tree is dirty so implement is partial (R5)"]
                  else reset_slice | .__notes += ["checkpoint says committed but branch has nothing past base and the tree is clean; slice reset (R5)"] end
              else . end
            # R6
            | if .phases.review.status == "done" and .phases.review.head_sha != null and .phases.review.head_sha != $o.head then
                .phases.review |= pend | .__notes += ["review passed at \(.phases.review.head_sha[0:7]) but HEAD is \($o.head[0:7]); review pending again (R6)"]
              else . end
            # R7
            | if $o.dirty then
                if (.phases.implement.status == "pending" or .phases.implement.status == "running") then .phases.implement.status = "partial" | .__notes += ["uncommitted work on \(.branch); executor resumes from the diff (R7)"]
                elif .phases.commit.status == "done" and ((.phases.fix.status // "pending") != "running") then .__human += ["uncommitted changes on \(.branch) after its commit; inspect `git -C \($o.worktree) status` and commit or discard before resuming (R7)"]
                else . end
              else . end
            # R8
            | if $o.pr != null then
                .pr = {number: $o.pr.number, url: $o.pr.url}
                | if .phases.publish.status != "done" then .phases.publish |= (.status="done" | .finished_at=$now | .note="PR found on GitHub (R8)") | .__notes += ["PR #\($o.pr.number) exists; publish marked done (R8)"] else . end
              elif .pr != null and $o.pr == null and $o.in_stack != null then
                .__notes += ["recorded PR #\(.pr.number) not found on GitHub; publish pending (R8)"] | .pr = null | .phases.publish |= pend
              else . end
            # R11
            | reduce (.phases | to_entries[] | .key as $k | select(.value.status=="skipped" and ((.value.note // "") | startswith("deferred:")) and (($runnable | index([$k])) != null)) | $k) as $p (.;
                .__notes += ["\($p) was deferred (\(.phases[$p].note // "")) and this \($env) session can run it; pending again (R11)"] | .phases[$p] |= pend | .phases[$p].note = null)
            # R9
            | if $o.in_stack == false and .status != "pending" then .__stack_add = true | .__notes += ["branch \(.branch) is not in the gh stack; run `gh stack add \(.branch)` from \(.base_branch) first (R9)"] else . end
          end
        | .next = derive_next | .status = (if .status=="blocked" and $cp.stopped==null then status_for_next else status_for_next end)
        ) as $ns
      | .cp.slices[$i] = ($ns | del(.__notes, .__note, .__human, .__stack_add))
      | .notes += ([$ns.__note // empty] + ($ns.__notes // []) | map("\($s.id): " + .))
      | .needs_human += (($ns.__human // []) | map("\($s.id): " + .))
      | if $ns.__stack_add == true then .stack_add += [$ns.branch] else . end)
  # R10
  | if $cp.trunk_sha != $trunk_now then .notes += ["trunk moved \($cp.trunk_sha[0:7]) -> \($trunk_now[0:7]) since scope; rebase is your call (R10)"] else . end
  # resume point
  | (.cp.slices | map(select(.next != "publish" and .next != null)) | first) as $first
  | (if $first != null then {slice: $first.id, phase: $first.next, attempt: (($first.phases[$first.next].attempt // 0) + 1), partial: ($first.phases[$first.next].status == "partial")}
     elif (.cp.slices | any(.next == "publish")) then {slice: null, phase: "publish", attempt: 1, partial: false}
     elif (.cp.slices | length) == 0 then {slice: null, phase: "scope", attempt: 1, partial: false}
     else {slice: null, phase: "done", attempt: 1, partial: false} end) as $rf
  | .resume_from = $rf
  | .checkout = (if $first != null then $first.branch else (.cp.slices | last | .branch) end)
  | .cp.current_slice = $rf.slice
  | .cp.phase = ($rf.phase | if .=="fix" then "implement" elif .=="fix-check" then "review" else . end)
  | .cp.stopped = null | .cp.resumes += (if $plan_only then 0 else 1 end) | .cp.environment = $env | .cp.worktree = $root
  | .cp.harness = (if $harness == "unknown" then .cp.harness else $harness end)
  | .cp.reconcile = {at: $now, notes: .notes}
  | .steps = (
      (if (.needs_human | length) > 0 then ["settle needs_human before dispatching anything"] else [] end)
      + (if .checkout != null then ["checkout \(.checkout) (gh stack checkout, or git checkout when the stack is not yet published)"] else [] end)
      + (.stack_add | map("gh stack add \(.)"))
      + (if $rf.phase == "scope" then ["run §1 scope: the checkpoint has no slices yet"]
         elif $rf.phase == "publish" then ["run §5 publish for every slice whose publish phase is pending"]
         elif $rf.phase == "done" then ["nothing to resume: every slice is published; write the report from `checkpoint.sh render`"]
         else ["dispatch \($rf.phase) for \($rf.slice) as attempt \($rf.attempt)\(if $rf.partial then " with the partial-work note (read `git diff` first, continue, do not restart)" else "" end)"]
             + ["skip: " + ([.cp.slices[] | select(.next == "publish" or .next == null) | .id] | if length == 0 then "nothing gated yet" else join(", ") + " already gated" end)]
         end))
' <<<"$state")"

if ! $plan_only; then
  timeline "$ticket" "**resumed** by $(orch_harness) in $(orch_env) → $(jq -r ".resume_from | \"\\(.slice // \"stack\") \\(.phase) attempt \\(.attempt)\"" <<<"$result")$(jq -r "if (.needs_human|length)>0 then \" — needs human: \" + (.needs_human|join(\"; \")) else \"\" end" <<<"$result")"
  jq '.cp' <<<"$result" | save_state "$ticket"
fi
jq '{ticket: .cp.ticket, resume_from, checkout, steps, needs_human, notes, slices: [.cp.slices[] | {id, status, next, head: (.head_sha // "")[0:7], commits: (.commits|length), open_findings: ([.findings[]|select(.resolved|not)]|length), pr: .pr.number}]}' <<<"$result"
[ "$(jq '.needs_human | length' <<<"$result")" = 0 ] || exit 4

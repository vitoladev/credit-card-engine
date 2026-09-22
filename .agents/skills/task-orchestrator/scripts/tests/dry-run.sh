#!/usr/bin/env bash
# Dry run of the checkpoint / envelope / resume trio against a fake
# two-slice ticket in a throwaway git repo. Host tools only (bash, jq, git).
#   bash .agents/skills/task-orchestrator/scripts/tests/dry-run.sh
# Exits non-zero on the first failed assertion.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/orch-dryrun.XXXXXX")"; trap 'rm -rf "$tmp"' EXIT
# no ORCH_ROUTING: lib.sh merges routing.json with the fake repo's overlay,
# which is the path a consuming repo takes
export ORCH_HARNESS=claude-code ORCH_ROOT="$tmp/repo" ORCH_STATE_DIR="$tmp/repo/docs/ai/executions"
T="ABC-900"; S1="ABC-901"; S2="ABC-902"; B1="abc-901-fake-api"; B2="abc-902-fake-ui"
cp="$here/checkpoint.sh"; env="$here/envelope.sh"; resume="$here/resume.sh"; route="$here/route.sh"
pass=0; fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { pass=$((pass+1)); echo "  ok  $*"; }
expect() { # description, actual, expected
  [ "$2" = "$3" ] && ok "$1" || fail "$1: got '$2', want '$3'"; }
expect_match() { # description, pattern, text
  grep -qF -- "$2" <<<"$3" && ok "$1" || fail "$1: no '$2' in: $3"; }
get() { "$cp" get "$T" "$1"; }
write_env() { # slice phase attempt agent extra-json
  local p extra="${5:-}"; [ -n "$extra" ] || extra='{}'
  p="$("$env" path "$T" "$1" "$2" "$3")"; mkdir -p "$(dirname "$p")"
  jq -n --arg t "$T" --arg s "$1" --arg p "$2" --argjson a "$3" --arg agent "$4" \
    "{envelope_version:1, ticket:\$t, slice:(if \$s==\"-\" then null else \$s end), phase:\$p, agent:\$agent, model:\"x\", attempt:\$a, status:\"done\", summary:\"fake\", commands:[{cmd:\"fake\",outcome:\"pass\"}]} + ($extra) + {complete:true}" > "$p"
  echo "$p"; }

echo "== fake repo"
git init -q -b main "$ORCH_ROOT"; cd "$ORCH_ROOT"
git config user.email t@t; git config user.name t; git config commit.gpgsign false
echo base > README.md; git add .; git commit -qm "chore: root"
echo 'docs/ai/executions/' > .gitignore && git add .gitignore && git commit -qm "chore: ignore run state"
mkdir -p .agents && cat > .agents/orchestrator.json <<'EOF'
{"bindings":{"tracker":{"kind":"github","id_pattern":"^ABC-[0-9]+$"},"standards_doc":"CONTRIBUTING.md","command":{"wrapper":"direct"},"review_bot":null}}
EOF
git add .agents && git commit -qm "chore: orchestrator bindings"

echo "== bindings"
expect "ticket shape comes from the overlay" "$("$cp" init NOPE-1 --title x 2>&1 >/dev/null | grep -c 'tracker.id_pattern')" "1"

echo "== routing table"
expect "implement/backend routes to executor on expensive tier" "$("$route" implement backend | jq -r '"\(.agent) \(.model)"')" "backend-executor opus"
expect "commit routes to committer on cheap tier"           "$("$route" commit | jq -r '"\(.agent) \(.model)"')" "committer haiku"
expect "review routes to code-reviewer on expensive tier"   "$("$route" review | jq -r '"\(.agent) \(.model)"')" "code-reviewer opus"
expect "verify/frontend routes on mid tier"                 "$("$route" verify frontend | jq -r '"\(.agent) \(.model)"')" "frontend-verifier sonnet"
expect "cursor executors inherit"                           "$("$route" implement frontend --harness cursor | jq -r .model)" "inherit"

echo "== scope: init + two slices"
"$cp" init "$T" --title "Fake two-slice ticket" >/dev/null
expect "harness detected from the session" "$(get .harness)" claude-code
"$cp" slice add "$T" "$S1" --label backend --branch "$B1" --title "fake api" >/dev/null
"$cp" slice add "$T" "$S2" --label frontend --branch "$B2" --title "fake ui" >/dev/null
expect "run phase is scope" "$(get .phase)" scope
expect "slice 2 bases on slice 1" "$(get '.slices[1].base_branch')" "$B1"
test -f "$ORCH_STATE_DIR/$T.json" && ok "checkpoint at docs/ai/executions/$T.json"
git checkout -qb "$B1"; "$cp" run-phase "$T" implement >/dev/null

echo "== slice 1: implement -> verify -> commit -> review (happy path)"
"$cp" phase "$T" "$S1" implement running --agent backend-executor --model opus >/dev/null
expect "implement running => implementing" "$(get '.slices[0].status')" implementing
echo 'func A(){}' > api.go
f="$(write_env "$S1" implement 1 backend-executor '{files_changed:["api.go"],unsatisfied:[],deviations:["used a flat file: fake"]}')"
expect "envelope validates" "$("$env" check "$T" "$S1" implement 1)" valid
"$cp" phase "$T" "$S1" implement done --envelope "$f" >/dev/null
expect "implement done => next verify" "$(get '.slices[0].next')" verify
expect "deviation folded from envelope" "$(get '.slices[0].deviations[0]')" "used a flat file: fake"
# envelope gate: a truncated envelope must not pass
"$cp" phase "$T" "$S1" verify running --agent backend-verifier --model sonnet >/dev/null
p="$("$env" path "$T" "$S1" verify 1)"; printf '{"envelope_version":1,"ticket":"%s","slice":"%s","phase":"verify","agent":"backend-verifier","attempt":1,"status":"done","summary":"cut off","commands":[],"criteria":[{"criterion":"a","verdict":"passed","evidence":"TestA","level":"test"' "$T" "$S1" > "$p"
rc=0; "$env" check "$T" "$S1" verify 1 >/dev/null || rc=$?; expect "truncated envelope is rejected" "$rc" 1
nudge="$("$env" nudge "$T" "$S1" verify 1)"; expect_match "nudge names the problem: $(head -c 60 <<<"$nudge")..." "truncated" "$nudge"
rc=0; "$cp" phase "$T" "$S1" verify done --envelope "$p" >/dev/null 2>&1 || rc=$?; expect "phase done refuses a bad envelope" "$rc" 1
# missing per-phase field
jq -n --arg t "$T" --arg s "$S1" '{envelope_version:1,ticket:$t,slice:$s,phase:"verify",agent:"backend-verifier",attempt:1,status:"done",summary:"x",commands:[],criteria:[],complete:true}' > "$p"
out="$("$env" check "$T" "$S1" verify 1 || true)"; expect_match "per-phase required field enforced ($out)" 'findings: required for phase verify' "$out"
# wrong attempt number is an identity mismatch
jq '.attempt=2 | .findings=[]' "$p" > "$p.x" && mv "$p.x" "$p"; out="$("$env" check "$T" "$S1" verify 1 || true)"; expect_match "stale/copied envelope rejected ($out)" 'identity mismatch' "$out"
f="$(write_env "$S1" verify 1 backend-verifier '{criteria:[{criterion:"GET /fake returns 200",verdict:"passed",evidence:"TestFake",level:"test",invalidated_by:["api.go"]}],findings:[]}')"
"$cp" phase "$T" "$S1" verify done --envelope "$f" >/dev/null
expect "verify clean => next commit" "$(get '.slices[0].next')" commit
expect "proven ledger row" "$(get '.slices[0].proven[0].evidence')" TestFake
"$cp" phase "$T" "$S1" commit running --agent committer --model haiku >/dev/null
git add api.go; git commit -qm "feat(api): fake"; sha1="$(git rev-parse HEAD)"
f="$(write_env "$S1" commit 1 committer "{commits:[\"$sha1\"]}")"; "$cp" phase "$T" "$S1" commit done --envelope "$f" >/dev/null
expect "commit sha recorded" "$(get '.slices[0].commits[0]')" "$sha1"
expect "head sha tracked" "$(get '.slices[0].head_sha')" "$sha1"
"$cp" phase "$T" "$S1" review running --agent code-reviewer --model opus >/dev/null
f="$(write_env "$S1" review 1 code-reviewer '{verdict:"pass",findings:[{severity:"P3",text:"nit"}]}')"; "$cp" phase "$T" "$S1" review done --envelope "$f" >/dev/null
expect "review pass => gated" "$(get '.slices[0].status')" gated
expect "P3 does not open a finding" "$(get '[.slices[0].findings[]|select(.resolved|not)]|length')" 0
expect "last envelope is the review" "$(get '.last_envelope.phase')" review
expect "journal has one line per write" "$(wc -l < "$ORCH_STATE_DIR/$T.jsonl" | tr -d ' ')" "$(jq -s length "$ORCH_STATE_DIR/$T.jsonl")"
tl="$ORCH_STATE_DIR/$T-EXECUTION.md"; test -f "$tl" && ok "timeline at $T-EXECUTION.md next to the checkpoint"
expect_match "timeline records the dispatch" "$S1 implement running → backend-executor/opus attempt 1" "$(cat "$tl")"
expect_match "timeline records the gate" "$S1 review done — gated; next publish" "$(cat "$tl")"
"$cp" log "$T" "pre-commit hook was red on an unrelated test; fixed as its own commit" >/dev/null
expect_match "free-text log line" "pre-commit hook was red" "$(cat "$tl")"

echo "== slice 2: verify finds a P1 -> fix round; then the session dies mid fix-check"
git checkout -qb "$B2"
"$cp" phase "$T" "$S2" implement running --agent frontend-executor --model opus >/dev/null
echo 'ui' > ui.tsx
f="$(write_env "$S2" implement 1 frontend-executor '{files_changed:["ui.tsx"],unsatisfied:[],deviations:[]}')"; "$cp" phase "$T" "$S2" implement done --envelope "$f" >/dev/null
"$cp" phase "$T" "$S2" verify running --agent frontend-verifier --model sonnet >/dev/null
f="$(write_env "$S2" verify 1 frontend-verifier '{criteria:[{criterion:"empty state renders",verdict:"failed",evidence:"no empty state",level:"spec",invalidated_by:["ui.tsx"]},{criterion:"list renders",verdict:"passed",evidence:"e2e/ui.spec.ts",level:"spec"}],findings:[]}')"
"$cp" phase "$T" "$S2" verify done --envelope "$f" >/dev/null
expect "failed criterion => next fix" "$(get '.slices[1].next')" fix
expect "failed criterion is an open P1" "$(get '[.slices[1].findings[]|select(.resolved|not)]|length')" 1
"$cp" phase "$T" "$S2" fix running --agent frontend-executor --model opus >/dev/null
expect "fix round counted" "$(get '.slices[1].fix_rounds')" 1
echo 'ui+empty' > ui.tsx
f="$(write_env "$S2" fix 1 frontend-executor '{files_changed:["ui.tsx"],unsatisfied:[],deviations:[]}')"; "$cp" phase "$T" "$S2" fix done --envelope "$f" >/dev/null
"$cp" phase "$T" "$S2" commit running --agent committer --model haiku >/dev/null
git add ui.tsx; git commit -qm "feat(web): fake ui"; sha2="$(git rev-parse HEAD)"
f="$(write_env "$S2" commit 1 committer "{commits:[\"$sha2\"]}")"; "$cp" phase "$T" "$S2" commit done --envelope "$f" >/dev/null
expect "commit after fix => next fix-check" "$(get '.slices[1].next')" fix-check
"$cp" phase "$T" "$S2" fix-check running --agent fix-checker --model haiku >/dev/null
# ---- the session rate-limits here: fix-check is `running`, no envelope ----
cp "$ORCH_STATE_DIR/$T.json" "$tmp/died-at.json"

echo "== resume A: fix-check died without an envelope (R3)"
plan="$("$resume" "$T" --offline)"
expect "resume picks slice 2" "$(jq -r .resume_from.slice <<<"$plan")" "$S2"
expect "resume phase is fix-check" "$(jq -r .resume_from.phase <<<"$plan")" fix-check
expect "next dispatch is attempt 2" "$(jq -r .resume_from.attempt <<<"$plan")" 2
expect "slice 1 is skipped" "$(jq -r '.steps[] | select(startswith("skip"))' <<<"$plan")" "skip: $S1 already gated"
expect "checkout target" "$(jq -r .checkout <<<"$plan")" "$B2"
expect "resumes counted" "$(get .resumes)" 1
expect_match "R3 note present" '(R3)' "$(jq -r '.notes[]' <<<"$plan")"

echo "== resume B: the agent had finished — its envelope is on disk but the checkpoint write was lost (R2)"
cp "$tmp/died-at.json" "$ORCH_STATE_DIR/$T.json"
write_env "$S2" fix-check 1 fix-checker '{verdict:"resolved",findings:[]}' >/dev/null
plan="$("$resume" "$T" --offline)"
expect "fix-check applied from disk" "$(get '.slices[1].phases["fix-check"].status')" done
expect "finding resolved" "$(get '[.slices[1].findings[]|select(.resolved|not)]|length')" 0
expect "verify-found finding => review still due" "$(jq -r .resume_from.phase <<<"$plan")" review
expect_match "R2 note present" '(R2)' "$(jq -r '.notes[]' <<<"$plan")"

echo "== resume C: review passed, then someone committed on top (R6) and a hotfix worktree went dirty (R7)"
"$cp" phase "$T" "$S2" review running --agent code-reviewer --model opus >/dev/null
f="$(write_env "$S2" review 1 code-reviewer '{verdict:"pass",findings:[]}')"; "$cp" phase "$T" "$S2" review done --envelope "$f" >/dev/null
expect "slice 2 gated" "$(get '.slices[1].status')" gated
echo 'more' >> ui.tsx; git commit -qam "fix(web): after review"
plan="$("$resume" "$T" --offline)"
expect "review invalidated by HEAD move" "$(get '.slices[1].phases.review.status')" pending
expect "resume goes back to review" "$(jq -r .resume_from.phase <<<"$plan")" review
expect_match "R6 note present" '(R6)' "$(jq -r '.notes[]' <<<"$plan")"
echo 'dirty' >> ui.tsx
rc=0; plan="$("$resume" "$T" --offline)" || rc=$?
expect "dirty tree after commit exits 4 (needs_human)" "$rc" 4
expect_match "R7 needs_human present" '(R7)' "$(jq -r '.needs_human[]' <<<"$plan")"
git checkout -q -- ui.tsx

echo "== resume D: committer ran but the checkpoint never saw it (R4), and slice 1's branch is deleted (R1)"
"$cp" phase "$T" "$S2" review running --agent code-reviewer --model opus >/dev/null
f="$(write_env "$S2" review 2 code-reviewer '{verdict:"pass",findings:[]}')"; "$cp" phase "$T" "$S2" review done --envelope "$f" >/dev/null
jq '.slices[1].phases.commit.status="pending" | .slices[1].commits=[]' "$ORCH_STATE_DIR/$T.json" > "$tmp/x" && mv "$tmp/x" "$ORCH_STATE_DIR/$T.json"
git checkout -q main; git branch -qD "$B1"
plan="$("$resume" "$T" --offline)"
expect "commit re-derived from git" "$(get '.slices[1].phases.commit.status')" done
expect "commit list re-derived (base_sha pinned, so slice 1 commits excluded)" "$(get '.slices[1].commits|length')" 2
expect "slice 1 reset after branch loss" "$(get '.slices[0].status')" pending
expect "resume restarts slice 1 implement" "$(jq -r '.resume_from | "\(.slice) \(.phase) \(.attempt)"' <<<"$plan")" "$S1 implement 2"
expect_match "R1 note present" '(R1)' "$(jq -r '.notes[]' <<<"$plan")"

echo "== atomic write: a crash mid-write leaves the previous checkpoint intact"
before="$(cat "$ORCH_STATE_DIR/$T.json")"
rc=0; "$cp" deviation "$T" "$S2" "" >/dev/null 2>&1 || rc=$?
[ "$rc" != 0 ] && [ "$(cat "$ORCH_STATE_DIR/$T.json")" = "$before" ] && ok "rejected write left the file untouched"
[ -z "$(ls "$ORCH_STATE_DIR"/*.tmp.* 2>/dev/null)" ] && ok "no tmp files left behind"
"$here/validate.sh" checkpoint "$ORCH_STATE_DIR/$T.json" >/dev/null && ok "final checkpoint validates"

echo "== cloud: a Claude cloud session implements slice 1 again, defers verify, mirrors state; a fresh clone resumes it"
git init -q --bare "$tmp/origin.git"; git remote add origin "$tmp/origin.git"; git push -q origin main
git checkout -qb "$B1" main
export ORCH_ENV=cloud ORCH_SYNC=1
expect "cloud env detected" "$(bash -c '. '"$here"'/lib.sh; orch_env')" cloud
rc=0; "$cp" phase "$T" "$S1" verify running --agent backend-verifier --model sonnet >/dev/null 2>&1 || rc=$?
expect "cloud refuses to run verify" "$rc" 1
"$cp" phase "$T" "$S1" implement running --agent backend-executor --model opus >/dev/null
echo 'func A(){}' > api.go
f="$(write_env "$S1" implement 2 backend-executor '{files_changed:["api.go"],unsatisfied:[],deviations:[]}')"; "$cp" phase "$T" "$S1" implement done --envelope "$f" >/dev/null
"$cp" phase "$T" "$S1" verify skipped --note deferred:cloud >/dev/null
expect "deferred verify moves on to commit" "$(get '.slices[0].next')" commit
"$cp" phase "$T" "$S1" commit running --agent committer --model haiku >/dev/null
git add api.go; git commit -qm "feat(api): fake again"; git push -q origin "$B1"
f="$(write_env "$S1" commit 2 committer "{commits:[\"$(git rev-parse HEAD)\"]}")"; "$cp" phase "$T" "$S1" commit done --envelope "$f" >/dev/null
expect "state mirrored to the orphan ref" "$("$here/sync.sh" list)" "$T"
# ---- the cloud sandbox is gone; a laptop clones fresh ----
unset ORCH_SYNC
git clone -q "$tmp/origin.git" "$tmp/clone"; export ORCH_ROOT="$tmp/clone" ORCH_STATE_DIR="$tmp/clone/docs/ai/executions" ORCH_ENV=local
git -C "$tmp/clone" checkout -q "$B1"
test ! -f "$ORCH_STATE_DIR/$T.json" && ok "fresh clone has no local checkpoint"
plan="$("$resume" "$T" --offline)"
test -f "$ORCH_STATE_DIR/$T.json" && ok "resume pulled the checkpoint from origin"
expect "envelopes travelled too" "$(ls "$ORCH_STATE_DIR/$T/envelopes" | wc -l | tr -d ' ')" "$(ls "$tmp/repo/docs/ai/executions/$T/envelopes" | wc -l | tr -d ' ')"
expect_match "timeline travelled too" "**resumed** by claude-code in local" "$(cat "$ORCH_STATE_DIR/$T-EXECUTION.md")"
expect "deferred verify re-opened locally (R11)" "$(get '.slices[0].phases.verify.status')" pending
expect "resume goes to slice 1 verify" "$(jq -r '.resume_from | "\(.slice) \(.phase)"' <<<"$plan")" "$S1 verify"
expect "environment recorded as local" "$(get .environment)" local
expect_match "R11 note present" '(R11)' "$(jq -r '.notes[]' <<<"$plan")"

echo "== another harness picks the run up: Cursor resumes what Claude Code started"
export ORCH_HARNESS=cursor
plan="$("$resume" "$T" --offline)"
expect "checkpoint re-stamped with the current harness" "$(get .harness)" cursor
expect "route follows the current harness" "$("$route" verify backend | jq -r .model)" "composer-2.5[fast=false]"
expect "resume point unchanged across harnesses" "$(jq -r '.resume_from | "\(.slice) \(.phase)"' <<<"$plan")" "$S1 verify"
expect_match "timeline names the harness" "**resumed** by cursor in local" "$(cat "$ORCH_STATE_DIR/$T-EXECUTION.md")"
export ORCH_HARNESS=unknown
"$resume" "$T" --offline --plan-only >/dev/null
expect "unknown harness keeps the recorded one" "$(get .harness)" cursor
export ORCH_HARNESS=claude-code
unset ORCH_ENV

echo "== render"
"$cp" render "$T" | head -12
echo; 
echo "== fix-round cap: a third slice burns gate.max_fix_rounds and blocks"
S3="ABC-903"; B3="abc-903-fake-cap"
cd "$ORCH_ROOT"; git checkout -q "$B1"; git checkout -qb "$B3"
"$cp" slice add "$T" "$S3" --label backend --branch "$B3" --title "cap" >/dev/null
"$cp" phase "$T" "$S3" implement running --agent backend-executor --model opus >/dev/null
echo 'cap' > cap.go
f="$(write_env "$S3" implement 1 backend-executor '{files_changed:["cap.go"],unsatisfied:[],deviations:[]}')"; "$cp" phase "$T" "$S3" implement done --envelope "$f" >/dev/null
"$cp" phase "$T" "$S3" verify running --agent backend-verifier --model sonnet >/dev/null
f="$(write_env "$S3" verify 1 backend-verifier '{criteria:[{criterion:"c",verdict:"failed",evidence:"nope",level:"spec",invalidated_by:["cap.go"]}],findings:[]}')"; "$cp" phase "$T" "$S3" verify done --envelope "$f" >/dev/null
max_fix="$("$here/bindings.sh" | jq -r .gate.max_fix_rounds)"
expect "default cap is 3" "$max_fix" 3
for r in 1 2 3; do
  expect "round $r: still allowed => next fix" "$(get '.slices[2] | "\(.status) \(.next)"')" "$([ $r -eq 1 ] && echo implemented || echo committed) fix"
  "$cp" phase "$T" "$S3" fix running --agent backend-executor --model opus >/dev/null
  expect "round $r counted" "$(get '.slices[2].fix_rounds')" "$r"
  echo "cap$r" > cap.go
  f="$(write_env "$S3" fix "$r" backend-executor '{files_changed:["cap.go"],unsatisfied:[],deviations:[]}')"; "$cp" phase "$T" "$S3" fix done --envelope "$f" >/dev/null
  "$cp" phase "$T" "$S3" commit running --agent committer --model haiku >/dev/null
  git add cap.go; git commit -qm "fix(api): cap round $r"
  f="$(write_env "$S3" commit "$r" committer "{commits:[\"$(git rev-parse HEAD)\"]}")"; "$cp" phase "$T" "$S3" commit done --envelope "$f" >/dev/null
  "$cp" phase "$T" "$S3" fix-check running --agent fix-checker --model haiku >/dev/null
  f="$(write_env "$S3" fix-check "$r" fix-checker '{verdict:"not-resolved",findings:[]}')"; "$cp" phase "$T" "$S3" fix-check done --envelope "$f" >/dev/null
done
expect "after the cap: blocked" "$(get '.slices[2].status')" blocked
expect "after the cap: next stays fix for a human" "$(get '.slices[2].next')" fix

echo "$pass assertions passed"

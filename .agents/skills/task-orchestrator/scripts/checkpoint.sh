#!/usr/bin/env bash
# Checkpoint writer for task-orchestrator runs. Every call rewrites
# <state_dir>/<ticket>.json atomically (see lib.sh save_state).
#
#   checkpoint.sh init <ticket> --title T [--harness claude-code|cursor] [--trunk main]
#   checkpoint.sh slice add <ticket> <slice> --label L --branch B [--base main] [--title T]
#   checkpoint.sh phase <ticket> <slice|-> <phase> running --agent A --model M [--attempt N]
#   checkpoint.sh phase <ticket> <slice|-> <phase> done --envelope <file>
#   checkpoint.sh phase <ticket> <slice|-> <phase> failed|skipped [--note ...]
#   checkpoint.sh run-phase <ticket> <phase>        # scope|implement|...|publish|done
#   checkpoint.sh pr <ticket> <slice> --number N --url U
#   checkpoint.sh deviation <ticket> <slice> <text>
#   checkpoint.sh log <ticket> <text>                # free-text timeline line in <ticket>-EXECUTION.md
#   checkpoint.sh stop <ticket> --reason R [--hint H]
#   checkpoint.sh show <ticket>                     # one-line-per-slice summary
#   checkpoint.sh render <ticket>                   # markdown trace from the checkpoint
#   checkpoint.sh get <ticket> <jq-filter>
#
# Slice phases and where they lead (`next`): implement → verify → commit →
# review → publish. A verify or review that leaves P0/P1 findings routes to
# fix → commit → fix-check, then back to review (verify findings) or straight
# to gated (review findings). Up to routing.json gate.max_fix_rounds fix
# rounds per slice (3 by default); the next open finding past that blocks it.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

cmd="${1:-}"; shift || true

# --key value option parsing into OPT_key variables
parse_opts() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --*) local k="${1#--}"; k="${k//-/_}"; printf -v "OPT_$k" '%s' "${2:-}"; shift 2 ;;
      *) die "unexpected argument '$1'" ;;
    esac
  done
}

empty_phase='{status:"pending",agent:null,model:null,attempt:0,started_at:null,finished_at:null,head_sha:null,envelope:null,note:null}'

case "$cmd" in
  init)
    ticket="${1:-}"; shift || true; require_ticket "$ticket"; parse_opts "$@"
    [ -f "$(state_path "$ticket")" ] && die "checkpoint for $ticket exists; use resume.sh $ticket, or delete it to start over"
    harness="${OPT_harness:-$(orch_harness)}"; case "$harness" in claude-code|cursor) ;; *) die "cannot tell the harness; pass --harness claude-code|cursor or export ORCH_HARNESS" ;; esac
    trunk="${OPT_trunk:-main}"; trunk_sha="$(git_sha "$trunk")"; [ -n "$trunk_sha" ] || die "trunk '$trunk' not found"
    jq -n --arg t "$ticket" --arg title "${OPT_title:-}" --arg h "$harness" --arg now "$(now)" --arg env "$(orch_env)" \
      --arg wt "$ROOT" --arg trunk "$trunk" --arg tsha "$trunk_sha" --arg rid "$(now)-$(head -c 3 /dev/urandom | od -An -tx1 | tr -d ' \n')" '{
        schema_version: 1, ticket: $t, title: $title, harness: $h, environment: $env, run_id: $rid, resumes: 0,
        started_at: $now, updated_at: $now, worktree: $wt, trunk: $trunk, trunk_sha: $tsha,
        phase: "scope", current_slice: null, slices: [], last_envelope: null, stopped: null
      }' | save_state "$ticket"
    { printf "# %s — %s\n\nRun started %s · harness %s · env %s\n\n## Timeline\n\n" "$ticket" "${OPT_title:-}" "$(now)" "$harness" "$(orch_env)"; } > "$(timeline_path "$ticket")"
    echo "initialised $(state_path "$ticket") and $(timeline_path "$ticket")" ;;

  slice)
    sub="${1:-}"; [ "$sub" = add ] || die "usage: slice add <ticket> <slice> --label L --branch B"
    ticket="${2:-}"; slice="${3:-}"; shift 3 || true; require_ticket "$ticket"; require_ticket "$slice"; parse_opts "$@"
    [ -n "${OPT_label:-}" ] && [ -n "${OPT_branch:-}" ] || die "--label and --branch are required"
    case "$OPT_label" in backend|contract|infra) route=verify-backend-output ;; frontend) route=verify-frontend-output ;; *) die "label must be backend|frontend|contract|infra" ;; esac
    [ "$OPT_label" = contract ] && route=contract-regen-nodiff; [ "$OPT_label" = infra ] && route=infra-synth
    state="$(load_state "$ticket")"
    base="${OPT_base:-$(jq -r 'if (.slices|length)==0 then .trunk else .slices[-1].branch end' <<<"$state")}"
    jq -e --arg s "$slice" '.slices[] | select(.id==$s)' <<<"$state" >/dev/null && die "slice $slice already recorded"
    jq --arg s "$slice" --arg title "${OPT_title:-}" --arg label "$OPT_label" --arg b "$OPT_branch" --arg base "$base" \
       --arg bsha "$(git_sha "$base")" --arg route "$route" "
      .slices += [{
        id: \$s, title: \$title, label: \$label, order: (.slices|length), branch: \$b, base_branch: \$base,
        base_sha: (if \$bsha==\"\" then null else \$bsha end), head_sha: null, worktree: null, verify_route: \$route,
        status: \"pending\", next: \"implement\",
        phases: {implement: $empty_phase, verify: $empty_phase, commit: $empty_phase, review: $empty_phase, publish: $empty_phase},
        fix_rounds: 0, commits: [], pr: null, proven: [], findings: [], deviations: []
      }] | if .current_slice==null then .current_slice=\$s else . end" <<<"$state" | save_state "$ticket"
    timeline "$ticket" "$slice [$OPT_label] planned on \`$OPT_branch\` <- \`$base\`"
    echo "added $slice ($OPT_label) on $OPT_branch <- $base" ;;

  phase)
    ticket="${1:-}"; slice="${2:-}"; phase="${3:-}"; status="${4:-}"; shift 4 || true
    require_ticket "$ticket"; parse_opts "$@"
    case "$phase" in implement|verify|commit|review|publish|fix|fix-check) ;; *) die "unknown phase '$phase'" ;; esac
    case "$status" in running|done|failed|skipped|partial) ;; *) die "status must be running|done|failed|skipped|partial" ;; esac
    if [ "$status" = running ] && ! env_can "$phase"; then
      die "phase $phase is '$(jq -r --arg e "$(orch_env)" --arg p "$phase" '.environments[$e].phases[$p]' "$ROUTING")' in a $(orch_env) session (routing.json environments); defer it with 'skipped --note deferred:$(orch_env)' or resume on a machine that can run it"
    fi
    state="$(load_state "$ticket")"
    if [ "$slice" = "-" ]; then
      # stack-level publish: fan the same transition over every gated slice
      slice="$(jq -r '[.slices[] | select(.status=="gated" or .status=="published") | .id] | join(" ")' <<<"$state")"
      [ -n "$slice" ] || die "no gated slice to publish"
    fi
    env_json=null
    if [ "$status" = done ] && [ "$phase" != publish ]; then
      [ -n "${OPT_envelope:-}" ] || die "a done transition needs --envelope <file> (the validated agent envelope)"
      "$ORCH_DIR/validate.sh" envelope "$OPT_envelope" >/dev/null || die "envelope $OPT_envelope does not validate; nudge the agent, do not mark done"
      env_json="$(cat "$OPT_envelope")"
    fi
    for s in $slice; do
      branch="$(jq -r --arg s "$s" '.slices[] | select(.id==$s) | .branch' <<<"$state")"; [ -n "$branch" ] || die "slice $s not in checkpoint"
      head="$(git_sha "$branch")"
      base_sha="$(git_sha "$(jq -r --arg s "$s" '.slices[] | select(.id==$s) | .base_branch' <<<"$state")")"
      max_fix="$(jq -r '.gate.max_fix_rounds // 3' "$ROUTING")"
      state="$(jq --arg s "$s" --arg base_sha "$base_sha" --arg p "$phase" --arg st "$status" --arg now "$(now)" --arg head "$head" --argjson max_fix "$max_fix" \
        --arg agent "${OPT_agent:-}" --arg model "${OPT_model:-}" --arg attempt "${OPT_attempt:-}" --arg note "${OPT_note:-}" \
        --arg envpath "${OPT_envelope:-}" --argjson env "$env_json" "
        def phase_rec: (.phases[\$p] // $empty_phase);
        def nn(\$x): if \$x==\"\" then null else \$x end;
        (.slices[] | select(.id==\$s)) |= (
          .phases[\$p] = (phase_rec
            | .status = \$st
            | if \$st==\"running\" then
                .agent = (nn(\$agent) // .agent) | .model = (nn(\$model) // .model)
                | .attempt = (if \$attempt==\"\" then .attempt + 1 else (\$attempt|tonumber) end)
                | .started_at = \$now | .finished_at = null | .envelope = null
              else .finished_at = \$now end
            | .head_sha = nn(\$head) | .note = (nn(\$note) // .note)
            | if \$env != null then .envelope = \$envpath | .agent = \$env.agent | .model = (\$env.model // .model) else . end)
          | .head_sha = nn(\$head) | .base_sha = (.base_sha // nn(\$base_sha))
          | if \$p==\"fix\" and \$st==\"running\" then .fix_rounds += 1 else . end
          | if \$env != null then
              .commits = (.commits + (\$env.commits // []) | unique)
              | .deviations = (.deviations + (\$env.deviations // []))
              | .proven = (.proven + [(\$env.criteria // [])[] | select(.verdict==\"passed\") | {criterion, evidence, level: (if .level==\"controlled-response\" then \"test\" else .level end), invalidated_by: (.invalidated_by // [])}])
              | .findings = (.findings + [(\$env.findings // [])[] | select(.severity==\"P0\" or .severity==\"P1\") | . + {source: \$p, resolved: false}]
                  + [(\$env.criteria // [])[] | select(.verdict==\"failed\") | {severity: \"P1\", text: (\"verify failed: \" + .criterion + \" — \" + .evidence), paths: (.invalidated_by // []), source: \"verify\", resolved: false}])
              | if \$p==\"fix-check\" and \$env.verdict==\"resolved\" then .findings |= map(.resolved = true) else . end
            else . end
          | (.findings | map(select(.resolved|not)) | length) as \$open
          | (if \$env != null then \$env.status else null end) as \$es
          # slice status + next phase
          | if \$st==\"running\" then
              .status = ({implement:\"implementing\",fix:\"implementing\",verify:\"verifying\",commit:\"committing\",review:\"reviewing\",\"fix-check\":\"reviewing\",publish:\"gated\"}[\$p])
              | .next = \$p
            elif \$st==\"failed\" or \$es==\"blocked\" or \$es==\"failed\" then
              .status = \"blocked\" | .next = \$p
            elif \$st==\"partial\" then .status = \"implementing\" | .next = \$p
            elif \$st==\"skipped\" then
              (if \$p==\"verify\" then .status=\"implemented\" | .next=\"commit\" else . end)
            else # done
              if \$p==\"implement\" then .status=\"implemented\" | .next=\"verify\"
              elif \$p==\"verify\" then (if \$open>0 then (if .fix_rounds<\$max_fix then .status=\"implemented\" | .next=\"fix\" else .status=\"blocked\" | .next=\"fix\" end) else .status=\"verified\" | .next=\"commit\" end)
              elif \$p==\"fix\" then .status=\"implemented\" | .next=\"commit\"
              elif \$p==\"commit\" then .status=\"committed\" | .next=(if .fix_rounds>0 and \$open>0 then \"fix-check\" else \"review\" end)
              elif \$p==\"review\" then (if \$open>0 or \$env.verdict==\"fail\" then (if .fix_rounds<\$max_fix then .status=\"committed\" | .next=\"fix\" else .status=\"blocked\" | .next=\"fix\" end) else .status=\"gated\" | .next=\"publish\" end)
              elif \$p==\"fix-check\" then (if \$env.verdict==\"resolved\" then (if .phases.review.status==\"done\" then .status=\"gated\" | .next=\"publish\" else .status=\"committed\" | .next=\"review\" end) elif .fix_rounds<\$max_fix then .status=\"committed\" | .next=\"fix\" else .status=\"blocked\" | .next=\"fix\" end)
              elif \$p==\"publish\" then .status=\"published\" | .next=null
              else . end
            end
        )
        | .current_slice = \$s
        | .phase = (if \$p==\"fix\" then \"implement\" elif \$p==\"fix-check\" then \"review\" else \$p end)
        | if \$env != null then .last_envelope = {path: \$envpath, slice: \$env.slice, phase: \$env.phase, status: \$env.status, at: \$now} else . end
        | .stopped = null" <<<"$state")"
    done
    printf "%s" "$state" | save_state "$ticket"
    for s in $slice; do
      row="$(jq -r --arg s "$s" --arg p "$phase" '.slices[] | select(.id==$s) | "\(.phases[$p].attempt) \(.status) \(.next // "-")"' "$(state_path "$ticket")")"
      read -r attempt sstatus snext <<<"$row"
      case "$status" in
        running) line="$s $phase running → ${OPT_agent:-?}/${OPT_model:-?} attempt $attempt" ;;
        *)       line="$s $phase $status — $sstatus; next $snext" ;;
      esac
      timeline "$ticket" "$line${OPT_note:+ — $OPT_note}"
    done
    jq -r --arg s "${slice%% *}" '.slices[] | select(.id==$s) | "\(.id) \(.status) next=\(.next // "-") head=\(.head_sha // "-")[0:7] open_findings=\([.findings[]|select(.resolved|not)]|length)"' "$(state_path "$ticket")" ;;

  run-phase)
    ticket="${1:-}"; phase="${2:-}"; require_ticket "$ticket"
    case "$phase" in scope|implement|verify|commit|review|publish|done|stopped) ;; *) die "bad run phase" ;; esac
    load_state "$ticket" | jq --arg p "$phase" '.phase = $p | if $p=="done" then .current_slice=null else . end' | save_state "$ticket"; timeline "$ticket" "run phase → $phase"; echo "$ticket phase=$phase" ;;

  pr)
    ticket="${1:-}"; slice="${2:-}"; shift 2 || true; require_ticket "$ticket"; parse_opts "$@"
    [ -n "${OPT_number:-}" ] && [ -n "${OPT_url:-}" ] || die "--number and --url are required"
    load_state "$ticket" | jq --arg s "$slice" --argjson n "$OPT_number" --arg u "$OPT_url" '(.slices[] | select(.id==$s) | .pr) = {number: $n, url: $u}' | save_state "$ticket"; timeline "$ticket" "$slice PR #$OPT_number $OPT_url"; echo "$slice pr=#$OPT_number" ;;

  deviation)
    ticket="${1:-}"; slice="${2:-}"; text="${3:-}"; require_ticket "$ticket"; [ -n "$text" ] || die "deviation text required"
    load_state "$ticket" | jq --arg s "$slice" --arg t "$text" '(.slices[] | select(.id==$s) | .deviations) += [$t]' | save_state "$ticket"; timeline "$ticket" "$slice deviation: $text"; echo "recorded" ;;

  stop)
    ticket="${1:-}"; shift || true; require_ticket "$ticket"; parse_opts "$@"
    [ -n "${OPT_reason:-}" ] || die "--reason required"
    load_state "$ticket" | jq --arg r "$OPT_reason" --arg h "${OPT_hint:-}" --arg now "$(now)" '.phase="stopped" | .stopped={at:$now, reason:$r, resume_hint:$h}' | save_state "$ticket"; timeline "$ticket" "**stopped** — $OPT_reason${OPT_hint:+ (resume: $OPT_hint)}"; echo "$ticket stopped: $OPT_reason" ;;

  log)
    ticket="${1:-}"; text="${2:-}"; require_ticket "$ticket"; [ -n "$text" ] || die "log text required"
    timeline "$ticket" "$text"; echo "logged" ;;

  show)
    ticket="${1:-}"; require_ticket "$ticket"
    load_state "$ticket" | jq -r '
      "\(.ticket) \(.title) — phase=\(.phase) current=\(.current_slice // "-") resumes=\(.resumes) stopped=\(.stopped.reason // "no")",
      (.slices[] | "  \(.id) [\(.label)] \(.branch) status=\(.status) next=\(.next // "-") head=\((.head_sha // "-------")[0:7]) fix_rounds=\(.fix_rounds) pr=\(.pr.number // "-")  " + ([.phases | to_entries[] | "\(.key)=\(.value.status)"] | join(" ")))' ;;

  get)
    ticket="${1:-}"; require_ticket "$ticket"; load_state "$ticket" | jq -r "${2:-.}" ;;

  render)
    ticket="${1:-}"; require_ticket "$ticket"
    load_state "$ticket" | jq -r '
      "# \(.ticket) — \(.title)\n\nRun \(.run_id) started \(.started_at), updated \(.updated_at), resumes \(.resumes), harness \(.harness)\nWorktree `\(.worktree)`, trunk `\(.trunk)` @ \(.trunk_sha[0:7])\n\n## Slices\n\n| Slice | Label | Branch | Verify route | State | Next | HEAD | PR |\n| --- | --- | --- | --- | --- | --- | --- | --- |",
      (.slices[] | "| \(.id) | \(.label) | \(.branch) | \(.verify_route) | \(.status) | \(.next // "-") | \((.head_sha // "-------")[0:7]) | \(.pr.url // "-") |"),
      "\n## Proven\n\n| Criterion | Slice | Evidence | Level | Invalidated by |\n| --- | --- | --- | --- | --- |",
      (.slices[] | .id as $s | .proven[] | "| \(.criterion) | \($s) | \(.evidence) | \(.level) | \(.invalidated_by | join(", ")) |"),
      "\n## Findings\n",
      (.slices[] | .id as $s | .findings[] | "- \($s) \(.severity) [\(.source // "?")] \(if .resolved then "resolved" else "open" end): \(.text)"),
      "\n## Deviations\n",
      (.slices[] | .id as $s | .deviations[] | "- \($s): \(.)"),
      "\n## Phases\n",
      (.slices[] | .id as $s | .phases | to_entries[] | "- \($s) \(.key): \(.value.status) \(.value.agent // "")/\(.value.model // "") attempt=\(.value.attempt) \(.value.finished_at // .value.started_at // "") \(.value.envelope // "")"),
      (if .stopped then "\n## Stopped\n\n\(.stopped.at): \(.stopped.reason)\n\(.stopped.resume_hint // "")" else "" end)' ;;

  *) sed -n '2,20p' "$0"; exit 2 ;;
esac

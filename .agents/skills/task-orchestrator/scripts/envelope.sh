#!/usr/bin/env bash
# Envelope contract helpers.
#   envelope.sh path   <ticket> <slice|-> <phase> <attempt>          -> the path the agent must write
#   envelope.sh check  <ticket> <slice|-> <phase> <attempt>          -> "valid" (0) | problems (1) | missing (3)
#   envelope.sh nudge  <ticket> <slice|-> <phase> <attempt>          -> the nudge message to send the agent
#   envelope.sh footer <ticket> <slice|-> <phase> <attempt> <agent> <model> -> packet footer to paste on dispatch
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
cmd="${1:-}"; ticket="${2:-}"; slice="${3:-}"; phase="${4:-}"; attempt="${5:-}"
[ -n "$cmd" ] && [ -n "$attempt" ] || { sed -n '2,7p' "$0" >&2; exit 2; }
require_ticket "$ticket"; [ "$slice" = "-" ] && slice_name="stack" || slice_name="$slice"
path="$(envelope_dir "$ticket")/$slice_name-$phase-$attempt.json"

case "$cmd" in
  path) echo "$path" ;;
  check)
    [ -f "$path" ] || { echo "missing: $path"; exit 3; }
    out="$("$ORCH_DIR/validate.sh" envelope "$path" 2>&1)" || { echo "$out"; exit 1; }
    # the file must be the one we asked for, not a stale or copied envelope
    mism="$(jq -r --arg t "$ticket" --arg s "$slice" --arg p "$phase" --argjson a "$attempt" '
      [ (if .ticket!=$t then "ticket \(.ticket) != \($t)" else empty end),
        (if ($s!="-" and .slice!=$s) or ($s=="-" and .slice!=null) then "slice \(.slice) != \($s)" else empty end),
        (if .phase!=$p then "phase \(.phase) != \($p)" else empty end),
        (if .attempt!=$a then "attempt \(.attempt) != \($a)" else empty end) ] | join("; ")' "$path")"
    [ -z "$mism" ] || { echo "identity mismatch: $mism"; exit 1; }
    echo valid ;;
  nudge)
    problem="$("$0" check "$ticket" "$slice" "$phase" "$attempt" 2>&1 || true)"
    [ "$problem" = valid ] && { echo "envelope is valid; no nudge needed" >&2; exit 0; }
    case "$problem" in missing:*) problem="missing" ;; *"not valid JSON"*) problem="truncated (not valid JSON)" ;; *) problem="incomplete: $(tr '\n' ';' <<<"$problem")" ;; esac
    jq -r --arg path "$path" --arg problem "$problem" --arg phase "$phase" '.envelope.nudge | gsub("\\{path\\}"; $path) | gsub("\\{problem\\}"; $problem) | gsub("\\{phase\\}"; $phase)' "$ROUTING" ;;
  footer)
    agent="${6:-}"; model="${7:-}"; [ -n "$agent" ] || die "footer needs <agent> <model>"
    req="$(jq -r --arg p "$phase" '(.required + (.["x-required-by-phase"][$p] // [])) | map("`" + . + "`") | join(", ")' "$ORCH_DIR/envelope.schema.json")"
    slice_json="$([ "$slice" = "-" ] && echo null || echo "\"$slice\"")"
    cat <<EOT
Envelope (mandatory, last thing you do): write ONE JSON file to
  $path
It is the only report the orchestrator reads; prose that is not in this file
does not count. Schema: $ORCH_DIR/envelope.schema.json. Fields
required for this dispatch: $req.
Identity fields must be exactly: "ticket": "$ticket", "slice": $slice_json,
"phase": "$phase", "agent": "$agent", "model": "$model", "attempt": $attempt.
Write "complete": true as the final field, only once every other field is
filled from tool results you observed in this session. Then check it with
  $ORCH_DIR/validate.sh envelope $path
and fix anything it prints. Stop after the envelope; do not start new work.
EOT
    ;;
  *) sed -n '2,7p' "$0" >&2; exit 2 ;;
esac

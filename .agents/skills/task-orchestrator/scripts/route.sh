#!/usr/bin/env bash
# route.sh <phase> [label] [--harness claude-code|cursor]
# Prints {"phase","agent","model","tier"} from routing.json. The orchestrator
# passes `model` explicitly on every dispatch; agent frontmatter is fallback.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
phase="${1:-}"; label="${2:-}"; harness="$(orch_harness)"
[ "${3:-}" = --harness ] && harness="${4:-$harness}"
[ "$label" = --harness ] && { harness="${3:-$harness}"; label=""; }
jq -e --arg h "$harness" ".tiers[\$h]" "$ROUTING" >/dev/null || die "no tiers for harness '$harness' in routing.json; pass --harness or export ORCH_HARNESS"
[ -n "$phase" ] || { echo "usage: route.sh <phase> [label] [--harness h]" >&2; exit 2; }
jq -e --arg p "$phase" '.phases[$p]' "$ROUTING" >/dev/null || die "no route for phase '$phase'"
jq -c --arg p "$phase" --arg l "$label" --arg h "$harness" '
  .phases[$p] as $ph | .tiers[$h] as $tiers
  | ($ph.agent | if type=="object" then (if $l=="" then error("phase \($p) routes by label; pass one") else .[$l] end) else . end) as $agent
  | {phase: $p, agent: $agent, tier: $ph.tier, model: (if $ph.tier==null then null else $tiers[$ph.tier] end), runs_in: ($ph.runs_in // "agent"), skills: ($ph.skills // null)}' "$ROUTING"

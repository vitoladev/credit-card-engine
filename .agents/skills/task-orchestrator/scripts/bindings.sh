#!/usr/bin/env bash
# bindings.sh [--check] [<path>]
#
# Print the effective task-orchestrator config: routing.json (the plugin's
# defaults, next to this script) with the consuming repo's
# .agents/orchestrator.json merged over it. Objects merge key by key, arrays
# and scalars in the overlay replace the default. `review_bot` is null or an
# object {trigger, author, check, verdict, skip_on}; the repo describes its
# bot, the plugin ships no named ones. A label set to null in the overlay is
# dropped: that concern is not scoped in the repo.
#
#   bindings.sh                 # merged JSON on stdout
#   bindings.sh review_bot      # one key (jq path, dots allowed)
#   bindings.sh --check         # exit 1 naming every required binding still null
#
# ORCH_ROOT overrides the repo root (default: git toplevel); ORCH_OVERLAY the
# overlay path (default: $ROOT/.agents/orchestrator.json). Host tools only:
# bash, jq, git.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${ORCH_ROOT:-$(git rev-parse --show-toplevel)}"
DEFAULTS="$HERE/../routing.json"
OVERLAY="${ORCH_OVERLAY:-$ROOT/.agents/orchestrator.json}"
command -v jq >/dev/null || { echo "bindings: need jq" >&2; exit 2; }

check=false; [ "${1:-}" = --check ] && { check=true; shift; }
path="${1:-}"

overlay='{}'
if [ -f "$OVERLAY" ]; then
  overlay="$(jq -c . "$OVERLAY")" || { echo "bindings: $OVERLAY is not valid JSON" >&2; exit 2; }
fi

merged="$(jq -c --argjson o "$overlay" '
  (. * $o)
  | .bindings.review_bot |= (
      if . == null then null
      elif type != "object" then error("review_bot must be null or an object {trigger, author, check, verdict, skip_on}")
      elif (.author // "") == "" then error("review_bot.author is required: the login the bot reviews and threads carry")
      else ({trigger: null, check: null, verdict: null, skip_on: ["docs-only"]} + .) end)
  | .bindings.labels |= with_entries(select(.value != null))
  | walk(if type == "object" then del(.["$comment"]) else . end)' "$DEFAULTS")"

if $check; then
  missing="$(jq -r '
    .bindings as $b
    | [ .bindings.required[] | . as $k | select(($b | getpath($k | split("."))) == null) ]
    | .[]' <<<"$merged")"
  if [ -n "$missing" ]; then
    printf 'bindings: unresolved required binding: %s\n' $missing >&2
    echo "bindings: write them to $OVERLAY (see routing.json .bindings for the shape)" >&2
    exit 1
  fi
  echo "bindings: resolved"; exit 0
fi

if [ -n "$path" ]; then
  jq -c --arg p "$path" '.bindings | getpath($p | split("."))' <<<"$merged"
else
  jq . <<<"$merged"
fi

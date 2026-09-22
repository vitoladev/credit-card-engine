#!/usr/bin/env bash
# validate.sh checkpoint <file>   validate.sh envelope <file>
# Exit 0 and print "valid" when the file passes its schema; otherwise print
# one violation per line and exit 1. Envelope validation also enforces the
# schema's per-phase required fields.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
kind="${1:-}"; file="${2:-}"
[ -n "$kind" ] && [ -f "$file" ] || { echo "usage: validate.sh checkpoint|envelope <file>" >&2; exit 2; }
schema="$here/$kind.schema.json"; [ -f "$schema" ] || { echo "no schema for '$kind'" >&2; exit 2; }
if ! jq -e . "$file" >/dev/null 2>&1; then echo "$file: not valid JSON (truncated?)"; exit 1; fi
errs="$(jq -r -n -L "$here" --slurpfile s "$schema" --slurpfile d "$file" \
  'include "validate"; $s[0] as $schema | $d[0] as $doc | check($doc; $schema; $schema; "$")')"
if [ "$kind" = envelope ]; then
  errs+="${errs:+
}$(jq -r -n --slurpfile s "$schema" --slurpfile d "$file" '
    $d[0] as $e | ($s[0]["x-required-by-phase"][$e.phase] // [])[]
    | . as $r | select(($e | has($r)) | not) | "$.\($r): required for phase \($e.phase)"')"
fi
if [ -n "$errs" ]; then printf '%s\n' "$errs"; exit 1; fi
echo valid

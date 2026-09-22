#!/usr/bin/env bash
# Exercises bindings.sh against throwaway overlays. Host tools only.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
B="$HERE/../bindings.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
ok()   { echo "ok   $1"; }
bad()  { echo "FAIL $1" >&2; fail=1; }
expect() { # name expected actual
  [ "$2" = "$3" ] && ok "$1" || { bad "$1: expected $2, got $3"; }; }

# no overlay: every required binding is reported, exit 1
if out="$(ORCH_ROOT="$T" "$B" --check 2>&1)"; then bad "no overlay should fail"; else
  for k in tracker.kind tracker.id_pattern standards_doc command.wrapper; do
    grep -q "unresolved required binding: $k" <<<"$out" && ok "reports $k" || bad "missing report for $k"; done; fi

# a full review_bot object passes through; defaults survive under the merge
cat > "$T/a.json" <<'EOF'
{"bindings":{"tracker":{"kind":"linear","team":"ABC","id_pattern":"^ABC-\\d+$"},"standards_doc":"docs/STANDARDS.md","command":{"wrapper":"scripts/exec.sh"},"review_bot":{"trigger":"gh pr comment $PR --body '@bot review'","author":"bot[bot]","check":"bot","verdict":"bot-approval","skip_on":["docs-only"]}}}
EOF
ORCH_OVERLAY="$T/a.json" "$B" --check >/dev/null && ok "bot overlay resolves" || bad "bot overlay should resolve"
expect "bot author kept" 'bot[bot]' "$(ORCH_OVERLAY="$T/a.json" "$B" review_bot.author | jq -r .)"
expect "bot verdict kept" bot-approval "$(ORCH_OVERLAY="$T/a.json" "$B" review_bot.verdict | jq -r .)"
expect "default host_only kept" '["git","gh"]' "$(ORCH_OVERLAY="$T/a.json" "$B" command.host_only)"
expect "default state_dir kept" docs/ai/executions "$(ORCH_OVERLAY="$T/a.json" "$B" state_dir | jq -r .)"

# a minimal review_bot (author only) gets the shape's defaults; arrays replace; nested objects merge
cat > "$T/b.json" <<'EOF'
{"bindings":{"tracker":{"kind":"github","id_pattern":"^#\\d+$"},"standards_doc":"CONTRIBUTING.md","command":{"wrapper":"direct","host_only":["git"]},"review_bot":{"author":"reviewer[bot]"},"labels":{"contract":"backend"}}}
EOF
expect "minimal bot: trigger defaults null" null "$(ORCH_OVERLAY="$T/b.json" "$B" review_bot.trigger)"
expect "minimal bot: verdict defaults null" null "$(ORCH_OVERLAY="$T/b.json" "$B" review_bot.verdict)"
expect "minimal bot: skip_on defaults" '["docs-only"]' "$(ORCH_OVERLAY="$T/b.json" "$B" review_bot.skip_on)"
expect "array replaces" '["git"]' "$(ORCH_OVERLAY="$T/b.json" "$B" command.host_only)"
expect "object merges" '{"backend":"backend","frontend":"frontend","contract":"backend","infra":"infra"}' "$(ORCH_OVERLAY="$T/b.json" "$B" labels)"

# a null label drops the concern; the others keep their defaults
echo '{"bindings":{"tracker":{"kind":"x","id_pattern":"y"},"standards_doc":"z","command":{"wrapper":"w"},"labels":{"contract":null}}}' > "$T/l.json"
expect "null label dropped" '{"backend":"backend","frontend":"frontend","infra":"infra"}' "$(ORCH_OVERLAY="$T/l.json" "$B" labels)"

# review_bot null is CI-only, not unresolved
echo '{"bindings":{"tracker":{"kind":"x","id_pattern":"y"},"standards_doc":"z","command":{"wrapper":"w"},"review_bot":null}}' > "$T/c.json"
ORCH_OVERLAY="$T/c.json" "$B" --check >/dev/null && ok "null review_bot resolves" || bad "null review_bot should resolve"
expect "null review_bot stays null" null "$(ORCH_OVERLAY="$T/c.json" "$B" review_bot)"

# a bot name as a string, or an object without author, is an error
echo '{"bindings":{"review_bot":"somebot"}}' > "$T/d.json"
if ORCH_OVERLAY="$T/d.json" "$B" >/dev/null 2>"$T/err"; then bad "string review_bot should fail"; else
  grep -q "review_bot must be null or an object" "$T/err" && ok "string review_bot rejected" || bad "string review_bot message"; fi
echo '{"bindings":{"review_bot":{"trigger":"x"}}}' > "$T/d2.json"
if ORCH_OVERLAY="$T/d2.json" "$B" >/dev/null 2>"$T/err"; then bad "authorless review_bot should fail"; else
  grep -q "review_bot.author is required" "$T/err" && ok "authorless review_bot rejected" || bad "authorless review_bot message"; fi

# invalid overlay JSON is an error, not silently ignored
echo '{nope' > "$T/e.json"
ORCH_OVERLAY="$T/e.json" "$B" >/dev/null 2>&1 && bad "invalid overlay should fail" || ok "invalid overlay fails"

# routing keys outside bindings pass through untouched and $comment is gone
expect "phases pass through" committer "$(ORCH_OVERLAY="$T/a.json" "$B" | jq -r .phases.commit.agent)"
expect "top-level comment stripped" false "$(ORCH_OVERLAY="$T/a.json" "$B" | jq 'has("$comment")')"

[ "$fail" -eq 0 ] && echo "bindings: all passed" || { echo "bindings: failures" >&2; exit 1; }

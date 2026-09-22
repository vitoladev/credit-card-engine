#!/usr/bin/env bash
# Shared pieces for the task-orchestrator state scripts. Host tools only:
# bash, jq, git, gh. Nothing here runs the repo's toolchain.
set -euo pipefail

ORCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${ORCH_ROOT:-$(git rev-parse --show-toplevel)}"

command -v jq >/dev/null || { echo "orchestrator: need jq" >&2; exit 2; }

# The effective routing: the plugin defaults with the repo's
# .agents/orchestrator.json merged over them (bindings.sh does the merge).
# ORCH_ROUTING points at a pre-merged file instead, for tests.
if [ -n "${ORCH_ROUTING:-}" ]; then ROUTING="$ORCH_ROUTING"
else
  ROUTING="$(mktemp -t orch-routing.XXXXXX)"
  "$ORCH_DIR/bindings.sh" > "$ROUTING" || { rm -f "$ROUTING"; exit 2; }
  trap 'rm -f "$ROUTING"' EXIT
fi
binding() { jq -r --arg p "$1" '.bindings | getpath($p | split(".")) // empty' "$ROUTING"; }
STATE_DIR="${ORCH_STATE_DIR:-$ROOT/$(binding state_dir)}"
[ "$STATE_DIR" != "$ROOT/" ] || STATE_DIR="$ROOT/docs/ai/executions"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
die() { echo "orchestrator: $*" >&2; exit 1; }

state_path() { echo "$STATE_DIR/$1.json"; }
envelope_dir() { echo "$STATE_DIR/$1/envelopes"; }
journal_path() { echo "$STATE_DIR/$1.jsonl"; }
timeline_path() { echo "$STATE_DIR/$1-EXECUTION.md"; }

# The human timeline: append-only markdown next to the checkpoint. Every
# transition adds a line; `checkpoint.sh log` adds free text. Never a source
# of truth — the checkpoint is — but the narrative a resume reads first.
timeline() { # ticket text
  local p; p="$(timeline_path "$1")"
  [ -f "$p" ] || printf '# %s\n\n## Timeline\n\n' "$1" > "$p"
  printf -- '- `%s` %s\n' "$(date -u +%H:%M)" "$2" >> "$p"
}

# Identifier shape comes from bindings.tracker.id_pattern; with no binding
# resolved yet, anything non-empty without whitespace or a slash is accepted.
require_ticket() {
  local pat; pat="$(binding tracker.id_pattern)"
  [ -n "$pat" ] || pat='^[^[:space:]/]+$'
  [[ "${1:-}" =~ $pat ]] || die "ticket must match tracker.id_pattern ($pat), got '${1:-}'"
}

load_state() { # ticket -> stdout json
  local p; p="$(state_path "$1")"
  [ -f "$p" ] || die "no checkpoint at $p (run 'checkpoint.sh init' first)"
  cat "$p"
}

# Atomic write: the new state lands in a tmp file next to the target and is
# renamed over it, so a reader never sees a half-written checkpoint and a
# crash mid-write leaves the previous one intact. Each write also appends a
# one-line journal entry so the sequence of transitions survives.
save_state() { # ticket json-on-stdin
  local p tmp; p="$(state_path "$1")"; tmp="$p.tmp.$$"
  mkdir -p "$STATE_DIR"
  jq --arg now "$(now)" '.updated_at = $now' > "$tmp" || { rm -f "$tmp"; die "state is not valid JSON"; }
  if ! errs="$("$ORCH_DIR/validate.sh" checkpoint "$tmp")"; then rm -f "$tmp"; die "refusing to write a checkpoint that fails the schema: $errs"; fi
  mv -f "$tmp" "$p"
  jq -c '{at: .updated_at, phase, current_slice, slices: [.slices[] | {id, status, head_sha}]}' "$p" >> "$(journal_path "$1")"
  # ORCH_SYNC=1 mirrors every checkpoint to refs/orchestrator/state on origin
  # (the skill sets it in cloud sessions, where the clone dies with the session)
  if [ "${ORCH_SYNC:-0}" = 1 ]; then "$ORCH_DIR/sync.sh" push "$1" >/dev/null || echo "orchestrator: state push failed; local checkpoint is written" >&2; fi
}

# Which harness is driving this session. ORCH_HARNESS overrides; Claude Code
# exports CLAUDECODE, Cursor exports CURSOR_AGENT or CURSOR_TRACE_ID. The
# checkpoint records the harness only so routing.json's `tiers` can be read
# and so the timeline says who ran what: any harness resumes any run.
orch_harness() {
  if [ -n "${ORCH_HARNESS:-}" ]; then echo "$ORCH_HARNESS"
  elif [ -n "${CLAUDECODE:-}" ]; then echo claude-code
  elif [ -n "${CURSOR_AGENT:-}${CURSOR_TRACE_ID:-}" ]; then echo cursor
  else echo unknown; fi
}

# Where the run executes. ORCH_ENV overrides; otherwise /sandbox is an
# OpenShell sandbox, no docker binary means a cloud session (Claude Code on
# the web, Cursor Cloud Agents), anything else is a laptop with the
# local runtime. routing.json's `environments` says what each may run.
orch_env() {
  if [ -n "${ORCH_ENV:-}" ]; then echo "$ORCH_ENV"
  elif [ "$ROOT" = /sandbox ]; then echo sandbox
  elif ! command -v docker >/dev/null 2>&1; then echo cloud
  else echo local; fi
}
env_can() { # phase -> exit 0 when this environment may run it
  jq -e --arg e "$(orch_env)" --arg p "$1" '.environments[$e].phases[$p] == "run"' "$ROUTING" >/dev/null
}

git_sha() { git -C "${2:-$ROOT}" rev-parse --verify --quiet "$1^{commit}" 2>/dev/null || true; }
branch_exists() { git -C "${2:-$ROOT}" show-ref --verify --quiet "refs/heads/$1"; }

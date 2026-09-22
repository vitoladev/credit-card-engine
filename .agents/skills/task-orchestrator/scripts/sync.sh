#!/usr/bin/env bash
# Mirror a ticket's state (checkpoint, journal, envelopes) to the orphan ref
# refs/orchestrator/state on origin so a session on another machine — a
# Claude cloud session cloning fresh, a laptop after the cloud one died — can
# resume it. Plain git plumbing: no worktree, no checkout, never touches the
# working tree or the current branch.
#
#   sync.sh push <ticket>     # commit <state_dir>/<ticket>* onto the ref and push
#   sync.sh pull <ticket>     # fetch the ref and write the ticket's files locally
#   sync.sh list              # tickets present on the remote ref
#
# Layout on the ref: <ticket>/checkpoint.json, <ticket>/journal.jsonl, <ticket>/EXECUTION.md,
# <ticket>/envelopes/<file>. Last writer wins per ticket; a rejected push is
# retried once on top of the newer remote ref.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
REF="refs/orchestrator/state"; REMOTE="${ORCH_REMOTE:-origin}"
cmd="${1:-}"; ticket="${2:-}"

fetch_ref() { git -C "$ROOT" fetch -q "$REMOTE" "+$REF:$REF" 2>/dev/null || true; }

build_and_push() {
  local parent tree idx commit; idx="$(mktemp)"; rm -f "$idx"
  parent="$(git -C "$ROOT" rev-parse -q --verify "$REF" || true)"
  export GIT_INDEX_FILE="$idx"
  if [ -n "$parent" ]; then git -C "$ROOT" read-tree "$parent"; git -C "$ROOT" rm -q -r --cached --ignore-unmatch "$ticket" >/dev/null; else git -C "$ROOT" read-tree --empty; fi
  local f rel
  for f in "$(state_path "$ticket")" "$(journal_path "$ticket")" "$(timeline_path "$ticket")" "$(envelope_dir "$ticket")"/*.json; do
    [ -f "$f" ] || continue
    case "$f" in
      "$(state_path "$ticket")") rel="$ticket/checkpoint.json" ;;
      "$(journal_path "$ticket")") rel="$ticket/journal.jsonl" ;;
      "$(timeline_path "$ticket")") rel="$ticket/EXECUTION.md" ;;
      *) rel="$ticket/envelopes/$(basename "$f")" ;;
    esac
    git -C "$ROOT" update-index --add --cacheinfo "100644,$(git -C "$ROOT" hash-object -w "$f"),$rel"
  done
  tree="$(git -C "$ROOT" write-tree)"; unset GIT_INDEX_FILE; rm -f "$idx"
  commit="$(printf 'orchestrator state: %s @ %s\n' "$ticket" "$(now)" | git -C "$ROOT" commit-tree "$tree" ${parent:+-p "$parent"})"
  git -C "$ROOT" update-ref "$REF" "$commit" ${parent:-}
  git -C "$ROOT" push -q "$REMOTE" "$REF:$REF"
}

case "$cmd" in
  push)
    require_ticket "$ticket"; [ -f "$(state_path "$ticket")" ] || die "nothing to push for $ticket"
    fetch_ref
    if ! build_and_push 2>/dev/null; then fetch_ref; build_and_push; fi
    echo "pushed $ticket to $REMOTE $REF" ;;
  pull)
    require_ticket "$ticket"; fetch_ref
    git -C "$ROOT" rev-parse -q --verify "$REF" >/dev/null || die "no $REF on $REMOTE"
    paths="$(git -C "$ROOT" ls-tree -r --name-only "$REF" -- "$ticket/" || true)"
    [ -n "$paths" ] || die "no state for $ticket on $REF"
    mkdir -p "$(envelope_dir "$ticket")"
    while IFS= read -r p; do
      case "$p" in
        "$ticket/checkpoint.json") dst="$(state_path "$ticket")" ;;
        "$ticket/journal.jsonl") dst="$(journal_path "$ticket")" ;;
        "$ticket/EXECUTION.md") dst="$(timeline_path "$ticket")" ;;
        "$ticket"/envelopes/*) dst="$(envelope_dir "$ticket")/$(basename "$p")" ;;
        *) continue ;;
      esac
      git -C "$ROOT" show "$REF:$p" > "$dst.tmp.$$" && mv -f "$dst.tmp.$$" "$dst"
    done <<<"$paths"
    echo "pulled $ticket from $REMOTE $REF ($(wc -l <<<"$paths" | tr -d ' ') files)" ;;
  list)
    fetch_ref; git -C "$ROOT" ls-tree --name-only "$REF" 2>/dev/null || echo "(none)" ;;
  *) sed -n '2,12p' "$0" >&2; exit 2 ;;
esac

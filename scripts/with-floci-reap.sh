#!/usr/bin/env bash
# Run a command, then always reap Floci Lambda containers (success, fail, or signal).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
reap() { bash "$root/scripts/floci-reap.sh" || true; }
trap reap EXIT
"$@"

#!/usr/bin/env bash
# Remove Floci's per-invocation Lambda containers for this stack.
# Leaves the floci sidecar (and any other compose/k8s Floci) running.
set -euo pipefail
if ! command -v docker >/dev/null 2>&1; then
  echo "floci-reap: docker not found; skip" >&2
  exit 0
fi
ids="$(docker ps -aq --filter label=io.floci.service=lambda --filter name=floci-CreditCardEngine || true)"
if [ -z "$ids" ]; then
  echo "floci-reap: no Lambda containers"
  exit 0
fi
n="$(printf '%s\n' "$ids" | grep -c .)"
# Floci may already be deleting a container we listed; that is not a failure.
printf '%s\n' "$ids" | xargs docker rm -f >/dev/null 2>&1 || true
echo "floci-reap: removed $n Lambda container(s)"

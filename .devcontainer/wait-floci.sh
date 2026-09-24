#!/usr/bin/env bash
set -euo pipefail
url="${AWS_ENDPOINT_URL:-http://floci:4566}/_localstack/health"
for _ in $(seq 1 40); do
	if curl -sf "$url" >/dev/null; then
		echo "Floci ready at ${AWS_ENDPOINT_URL:-http://floci:4566}"
		# Cursor forwards 4566 from this workspace container, not the Floci
		# sidecar. Listen here so Bruno on localhost reaches the stack.
		if ! pgrep -f 'socat TCP-LISTEN:4566' >/dev/null; then
			socat TCP-LISTEN:4566,fork,reuseaddr TCP:floci:4566 &
		fi
		exit 0
	fi
	sleep 1
done
echo "Floci did not become healthy at $url" >&2
exit 1

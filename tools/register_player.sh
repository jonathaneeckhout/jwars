#!/usr/bin/env bash
set -Eeuo pipefail

: "${JWARS_API_URL:=http://127.0.0.1:8080}"
JWARS_API_URL="${JWARS_API_URL%/}"
for dependency in curl jq; do
	if ! command -v "$dependency" >/dev/null 2>&1; then
		printf 'Required command not found: %s\n' "$dependency" >&2
		exit 2
	fi
done

if (($# != 0)); then
	printf 'Usage: %s\n' "$0" >&2
	exit 2
fi

curl --silent --show-error --fail-with-body \
	-X POST \
	-H 'Content-Type: application/json' \
	-d '{}' \
	"$JWARS_API_URL/v1/players" | jq .

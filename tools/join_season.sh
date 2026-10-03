#!/usr/bin/env bash
set -Eeuo pipefail

TOOLS_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$TOOLS_DIR/lib.sh"
jwars_load_config

if (($# != 0)); then
	printf 'Usage: %s\n' "$0" >&2
	exit 2
fi

curl --silent --show-error --fail-with-body \
	-X POST \
	-H "$(jwars_auth_header)" \
	-H 'Content-Type: application/json' \
	-d '{}' \
	"$JWARS_API_URL/v1/seasons/current/join" | jq .

#!/usr/bin/env bash
set -Eeuo pipefail

TOOLS_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$TOOLS_DIR/lib.sh"
jwars_load_config

if (($# != 2)) || [[ "$2" != soldier && "$2" != archer ]]; then
	printf 'Usage: %s <barracks-id> <soldier|archer>\n' "$0" >&2
	exit 2
fi

command_id="bash-$(date -u +%Y%m%dT%H%M%S)-$$-$RANDOM"
payload="$(jq -cn \
	--arg id "$command_id" \
	--arg building_id "$1" \
	--arg unit_kind "$2" \
	'{id:$id,type:"train",building_id:$building_id,unit_kind:$unit_kind}')"

response_file="$(mktemp)"
trap 'rm -f "$response_file"' EXIT
status="$(curl --silent --show-error \
	-o "$response_file" -w '%{http_code}' \
	-X POST \
	-H "$(jwars_auth_header)" \
	-H 'Content-Type: application/json' \
	--data-binary "$payload" \
	"$JWARS_API_URL/v1/commands")"

if jq -e . "$response_file" >/dev/null 2>&1; then
	jq . "$response_file"
else
	cat "$response_file"
fi

if [[ "$status" != 202 ]]; then
	printf 'Training request returned HTTP %s.\n' "$status" >&2
	exit 1
fi

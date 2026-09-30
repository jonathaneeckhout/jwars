#!/usr/bin/env bash
set -Eeuo pipefail

TOOLS_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$TOOLS_DIR/lib.sh"
jwars_load_config

if (($# < 2)); then
	printf 'Usage: %s <target-unit-id> <attacking-unit-id> [attacking-unit-id ...]\n' "$0" >&2
	exit 2
fi

target_id="$1"
shift
unit_ids_json="$(printf '%s\n' "$@" | jq -R . | jq -s .)"
command_id="bash-$(date -u +%Y%m%dT%H%M%S)-$$-$RANDOM"
payload="$(jq -cn \
	--arg id "$command_id" \
	--arg target_unit_id "$target_id" \
	--argjson unit_ids "$unit_ids_json" \
	'{id:$id,type:"attack",unit_ids:$unit_ids,target_unit_id:$target_unit_id}')"

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
	printf 'Attack request returned HTTP %s.\n' "$status" >&2
	exit 1
fi

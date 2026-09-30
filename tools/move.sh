#!/usr/bin/env bash
set -Eeuo pipefail

TOOLS_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$TOOLS_DIR/lib.sh"
jwars_load_config

if (($# < 3)); then
	printf 'Usage: %s <x> <y> <unit-id> [unit-id ...]\n' "$0" >&2
	exit 2
fi

x="$1"
y="$2"
shift 2
if [[ ! "$x" =~ ^[0-9]{1,3}$ || ! "$y" =~ ^[0-9]{1,3}$ ]]; then
	printf 'Coordinates must be whole numbers in the range 0..999.\n' >&2
	exit 2
fi
x=$((10#$x))
y=$((10#$y))
if ((x > 999 || y > 999)); then
	printf 'Coordinates must be whole numbers in the range 0..999.\n' >&2
	exit 2
fi

unit_ids_json="$(printf '%s\n' "$@" | jq -R . | jq -s .)"
command_id="bash-$(date -u +%Y%m%dT%H%M%S)-$$-$RANDOM"
payload="$(jq -cn \
	--arg id "$command_id" \
	--argjson x "$x" \
	--argjson y "$y" \
	--argjson unit_ids "$unit_ids_json" \
	'{id:$id,type:"move",unit_ids:$unit_ids,target:{x:$x,y:$y}}')"

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
	printf 'Move request returned HTTP %s.\n' "$status" >&2
	exit 1
fi

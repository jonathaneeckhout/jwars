#!/usr/bin/env bash
set -Eeuo pipefail

TOOLS_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "$TOOLS_DIR/lib.sh"
jwars_load_config

if (($# == 2)) && [[ "$1" == --after && "$2" =~ ^[0-9]+$ ]]; then
	cursor="$2"
elif (($# == 0)); then
	cursor=""
else
	printf 'Usage: %s [--after sequence]\n' "$0" >&2
	exit 2
fi

emit_envelope() {
	local event_name="$1" event_id="$2" json_data="$3"
	jq -cn \
		--arg event "$event_name" \
		--arg id "$event_id" \
		--argjson data "$json_data" \
		'{event:$event,id:$id,data:$data}'
}

if [[ -z "$cursor" ]]; then
	initial_state="$(curl --silent --show-error --fail \
		-H "$(jwars_auth_header)" \
		"$JWARS_API_URL/v1/world")"
	cursor="$(jq -er '.sequence | select(type == "number" and . >= 0)' <<<"$initial_state")"
	emit_envelope snapshot "$cursor" "$initial_state"
fi

stream_once() {
	local event_id="" event_name="message" data="" line="" status=""
	local stream_pid stream_fd curl_status

	coproc JWARS_STREAM {
		curl --no-buffer --silent --show-error --fail \
			--connect-timeout 10 \
			-H "$(jwars_auth_header)" \
			-w $'\n__JWARS_HTTP_STATUS__:%{http_code}\n' \
			"$JWARS_API_URL/v1/events?after=$cursor"
	}
	stream_pid="$JWARS_STREAM_PID"
	stream_fd="${JWARS_STREAM[0]}"

	while IFS= read -r line <&"$stream_fd"; do
		if [[ "$line" == __JWARS_HTTP_STATUS__:* ]]; then
			status="${line#*:}"
			continue
		fi
		case "$line" in
			id:*) event_id="${line#id:}"; event_id="${event_id# }" ;;
			event:*) event_name="${line#event:}"; event_name="${event_name# }" ;;
			data:*)
				line="${line#data:}"
				line="${line# }"
				data+="${line}"$'\n'
				;;
			"")
				if [[ -n "$data" ]]; then
					json_data="${data%$'\n'}"
					jq -e . >/dev/null <<<"$json_data"
					emit_envelope "$event_name" "$event_id" "$json_data"
					if [[ "$event_id" =~ ^[0-9]+$ ]]; then
						cursor="$event_id"
					fi
				fi
				event_id=""
				event_name="message"
				data=""
				;;
			:*) ;;
		esac
	done

	exec {stream_fd}<&-
	if wait "$stream_pid"; then
		curl_status=0
	else
		curl_status=$?
	fi
	JWARS_HTTP_STATUS="$status"
	JWARS_CURL_STATUS="$curl_status"
}

printf 'Listening to %s as player updates arrive. Press Ctrl-C to stop.\n' "$JWARS_API_URL" >&2
while :; do
	stream_once
	case "$JWARS_HTTP_STATUS" in
		200|000) ;;
		5[0-9][0-9]|429) ;;
		*)
			printf 'Event stream returned HTTP %s; check the URL and token.\n' "$JWARS_HTTP_STATUS" >&2
			exit 1
			;;
	esac
	if ((JWARS_CURL_STATUS != 0)) && [[ "$JWARS_HTTP_STATUS" != 000 ]]; then
		printf 'Event stream ended with curl status %s (HTTP %s).\n' "$JWARS_CURL_STATUS" "$JWARS_HTTP_STATUS" >&2
	fi
	printf 'Reconnecting after sequence %s in 1 second...\n' "$cursor" >&2
	sleep 1
done

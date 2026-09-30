#!/usr/bin/env bash

jwars_load_config() {
	: "${JWARS_API_URL:=http://127.0.0.1:8080}"
	JWARS_API_URL="${JWARS_API_URL%/}"
	if [[ -z "${JWARS_API_TOKEN:-}" ]]; then
		printf 'Set JWARS_API_TOKEN (for the local server, use dev-token).\n' >&2
		return 2
	fi
	for dependency in curl jq; do
		if ! command -v "$dependency" >/dev/null 2>&1; then
			printf 'Required command not found: %s\n' "$dependency" >&2
			return 2
		fi
	done
}

jwars_auth_header() {
	printf 'Authorization: Bearer %s' "$JWARS_API_TOKEN"
}

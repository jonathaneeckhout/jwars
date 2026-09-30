#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

for dependency in docker go; do
	if ! command -v "$dependency" >/dev/null 2>&1; then
		printf 'Required command not found: %s\n' "$dependency" >&2
		exit 2
	fi
done

if ! docker compose version >/dev/null 2>&1; then
	printf 'Docker Compose is required (try: docker compose version).\n' >&2
	exit 2
fi

if [[ ! -f .env ]]; then
	cp .env.example .env
	printf 'Created .env from .env.example.\n'
fi

# Load the same local settings the server uses. The checked-in example is shell-compatible.
set -a
# shellcheck disable=SC1091
source .env
set +a

printf 'Starting PostgreSQL...\n'
docker compose up -d db

printf 'Starting jwars at http://%s...\n' "${JWARS_ADDR:-127.0.0.1:8080}"
exec go run ./cmd/jwars

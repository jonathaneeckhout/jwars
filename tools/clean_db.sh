#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
	printf 'Docker Compose is required (try: docker compose version).\n' >&2
	exit 2
fi

printf 'Removing the local jwars database and all stored world data...\n'
docker compose down --volumes

printf 'Starting a fresh PostgreSQL database...\n'
docker compose up -d db

printf 'Database reset complete. Start jwars with ./tools/run.sh.\n'

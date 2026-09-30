#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

if ! command -v go >/dev/null 2>&1; then
	printf 'Required command not found: go\n' >&2
	exit 2
fi

exec go test -count=1 -v -timeout 300s ./tests/integration/...

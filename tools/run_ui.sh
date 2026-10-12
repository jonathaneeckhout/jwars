#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

if [[ -f .env ]]; then
	set -a
	# shellcheck disable=SC1091
	source .env
	set +a
fi

PYTHON="web/.venv/bin/python"
if [[ ! -x "$PYTHON" ]]; then
	cat >&2 <<'EOF'
NiceGUI virtual environment not found. Set it up with:
  python3 -m venv web/.venv
  web/.venv/bin/pip install -r web/requirements.txt
Then run ./tools/run_ui.sh again.
EOF
	exit 2
fi

if ! "$PYTHON" -c 'import nicegui, httpx' >/dev/null 2>&1; then
	cat >&2 <<'EOF'
NiceGUI dependencies are missing. Install them with:
  web/.venv/bin/pip install -r web/requirements.txt
EOF
	exit 2
fi

printf 'Starting JWARS player portal at http://%s:%s...\n' \
	"${JWARS_UI_HOST:-127.0.0.1}" "${JWARS_UI_PORT:-8081}"
exec env PYTHONPATH="$PROJECT_DIR/web${PYTHONPATH:+:$PYTHONPATH}" "$PYTHON" -m jwars_ui

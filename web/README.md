# JWARS player portal

The player-facing website runs on NiceGUI and Python. It calls the Go API over HTTP; it does not connect directly to PostgreSQL. It includes a landing page and scoreboard, API guide, registration and season enrollment, player profile and password controls, API token management, and an admin account panel. Browser gameplay is not included.

## Local start

Start the Go service first so it can create its PostgreSQL schema:

```sh
./tools/run_jwars.sh
```

In another terminal from the repository root, set up the Python environment once:

```sh
python3 -m venv web/.venv
. web/.venv/bin/activate
pip install -r web/requirements.txt
```

Then start the portal with `./tools/run_ui.sh`.

The portal listens on `http://127.0.0.1:8081` and calls the API at `http://127.0.0.1:8080` by default. On first Go server startup with an empty account table, it creates an `admin` account and prints a generated temporary password to the server log. Save it, sign in, and change it immediately.

If that admin loses access, an operator can use `go run ./cmd/jwars-admin reset-admin-password <username>` and share the temporary password directly.

## Configuration

- `JWARS_API_ORIGIN`: Go API address reachable from the Python service.
- `JWARS_UI_HOST` and `JWARS_UI_PORT`: NiceGUI bind address and port.
- `JWARS_UI_SESSION_SECRET`: stable random secret for browser session cookies; required when `JWARS_ENV=production`.
- `JWARS_UI_HTTPS_ONLY=true`: mark the session cookie Secure when the portal is behind HTTPS.
- `NICEGUI_STORAGE_PATH`: location for NiceGUI's server-side user storage. Keep it private; it contains player session credentials.

The web app stores account session tokens in NiceGUI's server-side per-user storage, not browser storage. The Go API stores only token hashes and password hashes. API token secrets are returned only at creation time.

# jwars

A persistent, server-authoritative RTS where players build bases and command units through APIs and autonomous clients.

## Run locally

Requires Go 1.25 or newer, Docker Compose, `curl`, and `jq` for the helper scripts. The server creates a development player and token, persists the world in PostgreSQL, and advances it once per second.

```sh
./tools/run_jwars.sh
```

The first run creates `.env` from `.env.example` if needed, starts PostgreSQL with Docker Compose, and launches the server. Stop the server with Ctrl+C; the database remains running for the next start. Other Bash API helpers are also in `tools/`.

Run the PostgreSQL-backed HTTP integration suite with `./tools/run_integration_tests.sh`. The suite lives in `tests/integration/`, starts a disposable PostgreSQL container through Testcontainers, and removes it when the test process exits; it does not touch the persistent development database.

The database schema is created by the server on startup. Migrations are intentionally omitted while the schema is evolving. After a schema change, stop the server, then reset the disposable local database before starting it again:

```sh
./tools/clean_db.sh
```

That removes all local player and world data in the Compose database volume and starts a fresh PostgreSQL instance. The old `data/world.json` file is not imported.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | `postgres://jwars:jwars@127.0.0.1:5432/jwars?sslmode=disable` | PostgreSQL connection string |
| `JWARS_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `JWARS_API_TOKEN` | `dev-token` | Development bearer token, stored as a SHA-256 hash |
| `JWARS_PLAYER_ID` | `player-1` | Player associated with the development token |
| `JWARS_BUILDING_DEFS` | `definitions/buildings.json` | Data-driven building definitions |

## API

All game endpoints require `Authorization: Bearer <token>`. The token resolves to a player; state, commands, and event cursors are player-scoped.

Get the current player-visible snapshot and available building definitions:

```sh
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/world
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/scoreboard
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/definitions/buildings
```

Entity IDs are opaque random 128-bit hex strings, so clients should read them from the snapshot or events rather than constructing them. Issue a move order with a discovered unit ID. Commands use stable unit IDs and a client-generated command ID; repeating the same command ID for that player returns the original result.

```sh
UNIT_ID="$(curl -fsS -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/world | jq -r '.units[0].id')"
curl -X POST http://127.0.0.1:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d "$(jq -cn --arg unit_id "$UNIT_ID" '{id:"order-001",type:"move",unit_ids:[$unit_id],target:{x:8,y:5}}')"
```

Issue a data-defined build order:

```sh
curl -X POST http://127.0.0.1:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"id":"build-001","type":"build","building_kind":"barracks","x":20,"y":20}'
```

Follow ordered updates. Pass `?after=<sequence>` when reconnecting; if the cursor is outside the retained replay window, the stream begins with a fresh snapshot.

```sh
curl -N -H 'Authorization: Bearer dev-token' \
  'http://127.0.0.1:8080/v1/events?after=0'
```

Building objects in snapshots include `build_ticks`, `progress_ticks`, and `progress_percent`. The event stream emits `buildings.progress` when a construction crosses 25%, 50%, or 75%; `buildings.completed` reports 100%. Progress still advances each world tick, so a snapshot contains the latest exact value between milestone events.

Snapshots always include your own units and buildings, plus enemy units and buildings currently within your vision. Resource deposits are listed only while visible. Workers and melee soldiers have 8 tiles of vision, archers have 10, the `base` has 12, and watchtowers have 12. Other completed buildings have 6. Distance uses Chebyshev distance, so diagonal movement counts as one tile. Buildings under construction do not provide vision. `units.spotted`, `buildings.spotted`, and `resource_deposits.spotted` reveal newly visible entities; `entities.hidden` contains only entity IDs and types when they leave vision.

Issue an explicit attack order against a currently visible enemy unit:

```sh
curl -X POST http://127.0.0.1:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"id":"attack-001","type":"attack","unit_ids":["<your-soldier-id>"],"target_unit_id":"<visible-enemy-unit-id>"}'
```

Workers cannot attack. Melee soldiers have 100 health and deal 20 damage per tick at one-tile range. Archers have 60 health and deal 10 damage per tick at four-tile range. Attackers pursue visible targets; when a target leaves vision, they move toward its last seen position and wait there until they see it again. Completed watchtowers deal 10 damage every five ticks to enemies within four tiles. Damage is applied simultaneously each tick. `units.damaged` and `units.destroyed` events report combat results. Units do not retaliate or heal automatically, and destroyed units do not respawn.

Workers gather one material every five ticks from visible deposits. Deposits contain 500 materials and regenerate one material per minute up to capacity. Issue a `gather` command with a visible `deposit_id`; workers walk within one tile and continue until given another order. A completed barracks trains one soldier (25 materials, 15 ticks) or archer (40 materials, 25 ticks) at a time. Issue a `train` command with the `building_id` and `unit_kind`. `training.started` and `training.completed` events report training, and snapshots include queue progress.

The current loop runs seven-day seasons around a fixed hill at (500, 500). Melee soldiers within its five-tile square control area score one point per minute when no rival melee soldiers are present; workers and archers do not count and contested control awards no points. `/v1/scoreboard` reports the season, hill, standings, and previous result. Snapshots include the season, hill state, and the authenticated player's score. `hill.changed` and point-milestone `score.changed` events are ordered with the player's other events. When a season expires, the server records the result, resets the world tick, units, buildings, resources, deposits, and score, then begins the next season while retaining player identities and API tokens. See [`gameloop.md`](gameloop.md) for the current loop definition.

Convenience Bash scripts for snapshots, scores and standings, listening, move and attack orders, and builds are in [`tools/`](tools/README.md).

## Current scope

The server stores players, token hashes, units, buildings, resources, deposits, training orders, command results, per-player event sequences, and a bounded event history in PostgreSQL. Tick and command writes are serialized by one Go process and persisted with their corresponding events in one transaction. Building footprints, costs, and construction times use data-driven definitions. World time pauses during server downtime and resumes from the last committed tick.

There is no player registration flow, building damage, terrain-based line of sight, remembered fog-of-war state, resource balancing, multi-process coordination, or automatic schema migration yet. The environment-seeded player/token is for local development; change it before using the server beyond localhost.

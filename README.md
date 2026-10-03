# jwars

A persistent, server-authoritative RTS where players build bases and command units through APIs and autonomous clients.

## Run locally

Requires Go 1.25 or newer, Docker Compose, `curl`, and `jq` for the helper scripts. The server creates a development player and token, persists the world in PostgreSQL, and advances it once per second during an active season. A new database waits for the next Monday 18:00 UTC start if the current season's enrollment window has already closed.

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
| `JWARS_VARIABLES_DIR` | `variables/` | JSON game settings and entity definitions |

## API

Player registration and current-season status are public. All game endpoints use `Authorization: Bearer <token>`; the token resolves to a player, and state, commands, and event cursors are player-scoped.

Check the current season and create a player during the 24-hour enrollment window. Registration returns a random player ID and a token once; save the token in the client because the server stores only its hash. The new player joins immediately and receives the standard starting base, worker, soldier, and 100 materials.

```sh
./tools/season.sh
./tools/register_player.sh
```

Players keep their ID and token between seasons, but must explicitly join each new season during its enrollment window:

```sh
JWARS_API_TOKEN='<saved-token>' ./tools/join_season.sh
```

Seasons begin Mondays at 18:00 UTC. Enrollment closes 24 hours later or when the 10-player cap is reached. A season still begins on schedule with fewer than five players. `variables/world.json` configures the schedule, enrollment window, player cap, balanced spawn locations, and relative starting-unit offsets. Returning players are assigned a different site where possible; assignments are randomized among available sites.

Get the current player-visible snapshot and available unit/building definitions:

```sh
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/world
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/scoreboard
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/definitions/buildings
curl -H 'Authorization: Bearer dev-token' http://127.0.0.1:8080/v1/definitions/units
```

Gameplay settings load once at startup from `variables/`: `buildings.json`, `units.json`, `economy.json`, and `world.json`. They include entity stats and costs, starting units/resources, deposit locations and rates, map size, tick interval, hill scoring, season schedule, player cap, and spawn locations. Starting-unit coordinates are offsets from the assigned spawn location; starting-building coordinates are offsets in `buildings.json`. Invalid or missing settings stop startup. Set `JWARS_VARIABLES_DIR` to select another directory; restart the server to apply edits. The building definitions remain available at `/v1/definitions/buildings`; unit stats and training costs are at `/v1/definitions/units`.

The current world and resource deposits are persisted in PostgreSQL. Run `./tools/clean_db.sh` once before using this configuration update because the schema changed and migrations are not implemented. Also reset the database after changing map size, starting placements, or deposit locations so persisted state matches those settings.

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

Snapshots always include your own units and buildings, plus enemy units and buildings currently within your vision. Resource deposits are listed only while visible. Workers and melee soldiers have 8 tiles of vision, archers have 10, the `base` has 12, and watchtowers have 12. Other completed buildings have 6. These values are configured in `variables/`. Distance uses Chebyshev distance, so diagonal movement counts as one tile. Buildings under construction do not provide vision. `units.spotted`, `buildings.spotted`, and `resource_deposits.spotted` reveal newly visible entities; `entities.hidden` contains only entity IDs and types when they leave vision.

Issue an explicit attack order against a currently visible enemy unit or destructible building:

```sh
curl -X POST http://127.0.0.1:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"id":"attack-001","type":"attack","unit_ids":["<your-soldier-id>"],"target_unit_id":"<visible-enemy-unit-id>"}'
```

For buildings, pass `target_building_id` or use `./tools/attack.sh --building <building-id> <attacking-unit-id> ...`.

Workers cannot attack. Melee soldiers have 100 health and deal 20 damage per tick at one-tile range. Archers have 60 health and deal 10 damage per tick at four-tile range. Attackers pursue visible targets; when a target leaves vision, they move toward its last seen position and wait there until they see it again. Barracks (300 health) and watchtowers (150 health) can be targeted and destroyed; the base is indestructible. Destroying a building cancels its queued training without refund. Completed watchtowers deal 10 damage every five ticks to enemies within four tiles. Damage is applied simultaneously each tick. `units.damaged`, `units.destroyed`, `buildings.damaged`, and `buildings.destroyed` events report combat results.

Workers gather one material every five ticks from visible deposits. Deposits contain 500 materials and regenerate one material per minute up to capacity. Issue a `gather` command with a visible `deposit_id`; workers walk within one tile and continue until given another order. A completed base trains workers (20 materials, 60 ticks); a completed barracks trains one soldier (25 materials, 15 ticks) or archer (40 materials, 25 ticks) at a time. If a player has no workers and cannot afford one, the base automatically starts a free emergency worker order that completes after 300 ticks. Issue a `train` command with the `building_id` and `unit_kind`. `training.started`, `training.completed`, and `training.cancelled` events report training, and snapshots include queue progress. Economy values and unit costs/timing are configured in `variables/economy.json` and `variables/units.json`.

The current loop runs seven-day seasons around a hill at (500, 500). Melee soldiers within its five-tile square control area score one point per minute when no rival melee soldiers are present; workers and archers do not count and contested control awards no points. `/v1/scoreboard` reports the season, hill, standings, and previous result. Snapshots include the season, hill state, and the authenticated player's score and whether they joined the season. `hill.changed` and point-milestone `score.changed` events are ordered with the player's other events. When a season expires, the server records the result and resets the world tick, units, buildings, resources, deposits, and score. Players explicitly rejoin during the next enrollment window, where the server seeds their new base and force. Hill, schedule, spawn, season, map, and tick settings are in `variables/world.json`. See [`gameloop.md`](gameloop.md) for the current loop definition.

Convenience Bash scripts for snapshots, scores and standings, listening, move and attack orders, gathering, training, and builds are in [`tools/`](tools/README.md).

## Current scope

The server stores players, token hashes, units, buildings, resources, deposits, training orders, command results, per-player event sequences, and a bounded event history in PostgreSQL. Tick and command writes are serialized by one Go process and persisted with their corresponding events in one transaction. Gameplay settings and entity stats load from JSON files under `variables/`. World time pauses during server downtime and resumes from the last committed tick.

There is no account recovery, terrain-based line of sight, remembered fog-of-war state, resource balancing, multi-process coordination, or automatic schema migration yet. The environment-seeded player/token is for local development; change it before using the server beyond localhost.

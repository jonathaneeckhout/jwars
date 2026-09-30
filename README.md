# jwars

A persistent, server-authoritative RTS where players build bases and command units through APIs and autonomous clients.

## Run locally

Requires Go 1.25 or newer, Docker Compose, `curl`, and `jq` for the helper scripts. The server creates a development player and token, persists the world in PostgreSQL, and advances it once per second.

```sh
./tools/run.sh
```

The first run creates `.env` from `.env.example` if needed, starts PostgreSQL with Docker Compose, and launches the server. Stop the server with Ctrl+C; the database remains running for the next start. Other Bash API helpers are also in `tools/`.

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
  -d '{"id":"build-001","type":"build","building_kind":"prototype","x":20,"y":20}'
```

Follow ordered updates. Pass `?after=<sequence>` when reconnecting; if the cursor is outside the retained replay window, the stream begins with a fresh snapshot.

```sh
curl -N -H 'Authorization: Bearer dev-token' \
  'http://127.0.0.1:8080/v1/events?after=0'
```

Building objects in snapshots include `build_ticks`, `progress_ticks`, and `progress_percent`. The event stream emits `buildings.progress` when a construction crosses 25%, 50%, or 75%; `buildings.completed` reports 100%. Progress still advances each world tick, so a snapshot contains the latest exact value between milestone events.

Convenience Bash scripts for snapshots, listening, move orders, and builds are in [`tools/`](tools/README.md).

## Current scope

The server stores players, token hashes, units, buildings, resources, command results, per-player event sequences, and a bounded event history in PostgreSQL. Tick and command writes are serialized by one Go process and persisted with their corresponding events in one transaction. Construction uses data-driven definitions; the included `prototype` definition is only a functional placeholder. World time pauses during server downtime and resumes from the last committed tick.

There is no player registration flow, combat, fog of war, resource balancing, multi-process coordination, or automatic schema migration yet. The environment-seeded player/token is for local development; change it before using the server beyond localhost.

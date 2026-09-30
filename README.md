# jwars

A persistent, server-authoritative RTS where players build bases and command units through APIs and autonomous clients.

## Run locally

Requires Go 1.25 or newer, Docker Compose, `curl`, and `jq` for the helper scripts. The server creates a development player and token, persists the world in PostgreSQL, and advances it once per second.

```sh
./tools/run.sh
```

The first run creates `.env` from `.env.example` if needed, starts PostgreSQL with Docker Compose, and launches the server. Stop the server with Ctrl+C; the database remains running for the next start. Other Bash API helpers are also in `tools/`.

The database schema is created by the server on startup. Migrations are intentionally omitted while the schema is evolving. To reset the disposable local database after a schema change:

```sh
docker compose down -v
docker compose up -d db
```

That removes all local player and world data in the Compose database volume. The old `data/world.json` file is not imported.

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

Issue a move order. Commands use stable unit IDs and a client-generated ID; repeating the same ID for that player returns the original result.

```sh
curl -X POST http://127.0.0.1:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"id":"order-001","type":"move","unit_ids":["player-1-worker-1"],"target":{"x":8,"y":5}}'
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

Convenience Bash scripts for snapshots, listening, move orders, and builds are in [`tools/`](tools/README.md).

## Current scope

The server stores players, token hashes, units, buildings, resources, command results, per-player event sequences, and a bounded event history in PostgreSQL. Tick and command writes are serialized by one Go process and persisted with their corresponding events in one transaction. Construction uses data-driven definitions; the included `prototype` definition is only a functional placeholder. World time pauses during server downtime and resumes from the last committed tick.

There is no player registration flow, combat, fog of war, resource balancing, multi-process coordination, or automatic schema migration yet. The environment-seeded player/token is for local development; change it before using the server beyond localhost.

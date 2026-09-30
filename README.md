# jwars

A persistent, server-authoritative RTS where players build bases and command units through APIs and autonomous clients.

## Run locally

Requires Go 1.23 or newer. The server creates a demo world, stores it in `data/world.json`, and advances units once per second.

```sh
go run ./cmd/jwars
```

The local development token is `dev-token`. Set `JWARS_API_TOKEN` before exposing the server anywhere beyond your own machine. Other settings:

| Variable | Default | Purpose |
| --- | --- | --- |
| `JWARS_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `JWARS_API_TOKEN` | `dev-token` | Bearer token required by game endpoints |
| `JWARS_PLAYER_ID` | `player-1` | Player served by this API instance |
| `JWARS_DATA_FILE` | `data/world.json` | Local durable world state |

## API slice

Every game endpoint requires `Authorization: Bearer <token>`.

Get the current player-visible snapshot:

```sh
curl -H 'Authorization: Bearer dev-token' http://localhost:8080/v1/world
```

Issue a move order. Commands use stable unit IDs and a client-generated ID; repeating an ID returns the original result rather than applying the order again.

```sh
curl -X POST http://localhost:8080/v1/commands \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"id":"order-001","type":"move","unit_ids":["worker-1"],"target":{"x":8,"y":5}}'
```

Follow ordered state updates. Pass `?after=<sequence>` when reconnecting; if the cursor is outside the retained replay window, the stream begins with a fresh snapshot.

```sh
curl -N -H 'Authorization: Bearer dev-token' \
  'http://localhost:8080/v1/events?after=0'
```

Convenience Bash scripts for snapshots, listening, and move orders are in [`tools/`](tools/README.md).

The prototype map is a 1000 by 1000 integer grid. Units move one tile per tick, preferring horizontal movement before vertical movement. Updates are player-scoped and have independent sequence numbers.

## Current scope

This is an early single-process slice: one configured player, demo units, move orders, snapshots, replayable SSE updates, and atomic local JSON saves. The persistence code is isolated in the world package for a later PostgreSQL implementation. Combat, construction, resource production, account management, fog of war, and multi-instance operation are not implemented yet.

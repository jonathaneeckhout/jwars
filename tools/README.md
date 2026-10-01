# API helper scripts

These Bash helpers call the local jwars API. They require `curl`, `jq`, and Bash.

```sh
export JWARS_API_URL=http://127.0.0.1:8080
export JWARS_API_TOKEN=dev-token
```

Start the game server in another terminal with `./tools/run_jwars.sh` from the repository root.

## Reset the local database

```sh
./tools/clean_db.sh
```

This removes the Compose database volume, including all players and world state, then starts an empty PostgreSQL database. Restart the server afterward so it initializes the fresh world.

## Run integration tests

```sh
./tools/run_integration_tests.sh
```

This runs the HTTP and PostgreSQL integration suite in `tests/integration/` against a temporary PostgreSQL container, then removes it. It requires Go and a working Docker-compatible container runtime. `-count=1` makes sure each invocation runs the tests instead of reusing Go's test cache. You can also run it with `go test -count=1 ./tests/integration/...`.

## Read the current state

```sh
./tools/state.sh
```

Print your current season score as a number, or fetch the full standings:

```sh
./tools/score.sh
./tools/scoreboard.sh
```

## Listen for updates

```sh
./tools/listen.sh
```

The listener prints a snapshot first, then one compact JSON envelope per SSE update. It reconnects from the last event sequence it printed. To resume manually from a known sequence:

```sh
./tools/listen.sh --after 12
```

## Move units

Coordinates are whole numbers from `0` through `999`; provide one or more unit IDs:

```sh
./tools/state.sh | jq '.units[] | {id, kind, x, y}'
./tools/move.sh 8 5 <worker-id> <soldier-id>
```

Entity IDs are random opaque values. Discover them from a state snapshot or events; don't guess or construct them.

The response includes the command ID and whether the server accepted the order. Rejections are printed with their reason and the script exits non-zero.

## Attack an enemy unit

Enemy units must currently be visible in your snapshot. Select a discovered enemy unit ID and one or more of your soldiers:

```sh
./tools/state.sh | jq '.units[] | {id, owner_id, kind, x, y, health}'
./tools/attack.sh <enemy-unit-id> <soldier-id> [soldier-id ...]
```

Soldiers deal 20 damage per tick at a range of one tile. Workers cannot attack. Attackers pursue visible targets and move toward a target's last seen position if it leaves vision. Snapshots include your entities and currently visible enemy entities; `entities.hidden` events tell listeners when an enemy leaves vision.

## Build a structure

List the server-defined building types, then issue a build order with a type and top-left map position:

```sh
curl -H "Authorization: Bearer $JWARS_API_TOKEN" \
  "$JWARS_API_URL/v1/definitions/buildings" | jq .
./tools/build.sh prototype 20 20
```

The server validates the definition, resource balance, queue, footprint, and map occupancy. The response confirms the accepted order. `listen.sh` receives `buildings.progress` events at 25%, 50%, and 75%, then a `buildings.completed` event at 100%. The state snapshot includes exact current progress between events.

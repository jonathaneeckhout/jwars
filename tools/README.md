# API helper scripts

These Bash helpers call the local jwars API. They require `curl`, `jq`, and Bash.

```sh
export JWARS_API_URL=http://127.0.0.1:8080
export JWARS_API_TOKEN=dev-token
```

Start the game server in another terminal with `./tools/run.sh` from the repository root.

## Reset the local database

```sh
./tools/clean_db.sh
```

This removes the Compose database volume, including all players and world state, then starts an empty PostgreSQL database. Restart the server afterward so it initializes the fresh world.

## Read the current state

```sh
./tools/state.sh
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
./tools/move.sh 8 5 player-1-worker-1 player-1-soldier-1
```

The response includes the command ID and whether the server accepted the order. Rejections are printed with their reason and the script exits non-zero.

## Build a structure

List the server-defined building types, then issue a build order with a type and top-left map position:

```sh
curl -H "Authorization: Bearer $JWARS_API_TOKEN" \
  "$JWARS_API_URL/v1/definitions/buildings" | jq .
./tools/build.sh prototype 20 20
```

The server validates the definition, resource balance, queue, footprint, and map occupancy. The response confirms the accepted order; `listen.sh` reports when construction completes.

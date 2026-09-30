# API helper scripts

These Bash helpers call the local jwars API. They require `curl`, `jq`, and Bash.

```sh
export JWARS_API_URL=http://127.0.0.1:8080
export JWARS_API_TOKEN=dev-token
```

Start the game server in another terminal with `go run ./cmd/jwars`.

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
./tools/move.sh 8 5 worker-1 soldier-1
```

The response includes the command ID and whether the server accepted the order. Rejections are printed with their reason and the script exits non-zero.

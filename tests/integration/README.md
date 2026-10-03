# Integration tests

Run this suite with `./tools/run_integration_tests.sh`. `main_test.go` starts one disposable PostgreSQL container and the API server for the suite; `helpers_test.go` contains shared setup and HTTP helpers.

| File | Coverage |
| --- | --- |
| `world_api_test.go` | Authentication, random entity IDs, and configured unit definitions |
| `commands_test.go` | Move acceptance, idempotency, movement, and rejection |
| `construction_test.go` | Snapshot progress, milestone and completion events, SSE replay, and persistence across world reinitialization |
| `combat_test.go` | Unit combat, visibility, building damage/destruction, training cancellation, and indestructible bases |
| `economy_test.go` | Resource gathering, soldier/archer/worker training, worker recovery, and watchtower attacks |
| `season_test.go` | Hill-control scoring and season reset |

`testdata/variables/` contains the integration suite's settings. Its `buildings.json` defines a four-tick test building so construction can be checked without waiting on the production duration.

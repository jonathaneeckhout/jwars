# Integration tests

Run this suite with `./tools/run_integration_tests.sh`. `main_test.go` starts one disposable PostgreSQL container and the API server for the suite; `helpers_test.go` contains shared setup and HTTP helpers.

| File | Coverage |
| --- | --- |
| `world_api_test.go` | Authentication, random entity IDs, and configured unit definitions |
| `commands_test.go` | Move acceptance, idempotency, movement, and rejection |
| `construction_test.go` | Snapshot progress, milestone and completion events, SSE replay, and persistence across world reinitialization |
| `economy_test.go` | Resource gathering, unit training, and watchtower attacks |

`testdata/variables/` contains the integration suite's settings. Its `buildings.json` defines a four-tick test building so construction can be checked without waiting on the production duration.

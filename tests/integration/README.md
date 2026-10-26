# Integration tests

Run this suite with `./tools/run_integration_tests.sh`. `main_test.go` starts one disposable PostgreSQL container and the API server for the suite; `helpers_test.go` contains shared setup and HTTP helpers.

| File | Coverage |
| --- | --- |
| `database_auth_test.go` | Password connections, managed identity selection, token renewal, identity failures/recovery, rejected tokens, and verified TLS |
| `world_api_test.go` | Authentication, random entity IDs, and configured unit definitions |
| `commands_test.go` | Move acceptance, idempotency, movement, and rejection |
| `construction_test.go` | Snapshot progress, milestone and completion events, SSE replay, and persistence across world reinitialization |
| `combat_test.go` | Unit combat, visibility, building damage/destruction, training cancellation, and indestructible bases |
| `economy_test.go` | Resource gathering, soldier/archer/worker training, worker recovery, and watchtower attacks |
| `season_test.go` | Hill-control scoring and season reset |

`testdata/variables/` contains the integration suite's settings. Its `buildings.json` defines a four-tick test building so construction can be checked without waiting on the production duration.

Database authentication tests exercise the public `database.NewPool` function through the actual Azure Identity SDK and a second disposable PostgreSQL container with generated TLS certificates. A local HTTP server implements the Container Apps identity endpoint. The returned opaque tokens are PostgreSQL SCRAM passwords, allowing the tests to prove that token selection and renewal work on real connections without Azure credentials. These tests do not validate Entra token signatures, Azure role assignments, or company networking.

Run only database authentication integration tests with:

```sh
go test -count=1 -v -timeout 120s ./tests/integration -run TestDatabaseAuthentication
```

Tests set their own identity environment variables and clean up the database, local endpoint, and temporary certificates. They never connect to the deployed database.

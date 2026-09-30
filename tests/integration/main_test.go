package integration_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jojo/jwars/internal/api"
	"github.com/jojo/jwars/internal/world"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var (
	testServer           *httptest.Server
	testPool             *pgxpool.Pool
	testWorld            *world.World
	testBuildingDefsPath string
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	containerContext, cancelContainerContext := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelContainerContext()

	postgresContainer, err := postgres.Run(containerContext, "postgres:17-alpine",
		postgres.WithDatabase("jwars_test"),
		postgres.WithUsername("jwars"),
		postgres.WithPassword("jwars"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start PostgreSQL test container: %v\n", err)
		return 1
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := postgresContainer.Terminate(cleanupContext); err != nil {
			fmt.Fprintf(os.Stderr, "stop PostgreSQL test container: %v\n", err)
		}
	}()

	databaseURL, err := postgresContainer.ConnectionString(containerContext, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "get PostgreSQL test connection string: %v\n", err)
		return 1
	}
	testPool, err = pgxpool.New(containerContext, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to PostgreSQL test container: %v\n", err)
		return 1
	}
	defer testPool.Close()
	if err := testPool.Ping(containerContext); err != nil {
		fmt.Fprintf(os.Stderr, "ping PostgreSQL test container: %v\n", err)
		return 1
	}

	testBuildingDefsPath = filepath.Join("testdata", "buildings.json")
	testWorld, err = world.New(testPool, testBuildingDefsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create test world: %v\n", err)
		return 1
	}
	if err := testWorld.Initialize(containerContext, "integration-bootstrap", "integration-bootstrap-token"); err != nil {
		fmt.Fprintf(os.Stderr, "initialize test database: %v\n", err)
		return 1
	}

	testServer = httptest.NewServer(api.New(testWorld))
	defer testServer.Close()
	return m.Run()
}

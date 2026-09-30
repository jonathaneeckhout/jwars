package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jojo/jwars/internal/api"
	"github.com/jojo/jwars/internal/world"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	databaseURL := env("DATABASE_URL", "postgres://jwars:jwars@127.0.0.1:5432/jwars?sslmode=disable")
	playerID := env("JWARS_PLAYER_ID", "player-1")
	token := env("JWARS_API_TOKEN", "dev-token")
	addr := env("JWARS_ADDR", "127.0.0.1:8080")
	definitionsPath := env("JWARS_BUILDING_DEFS", "definitions/buildings.json")

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelConnect()
	pool, err := pgxpool.New(connectCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := waitForPostgres(connectCtx, pool); err != nil {
		return err
	}

	w, err := world.New(pool, definitionsPath)
	if err != nil {
		return err
	}
	if err := w.Initialize(connectCtx, playerID, token); err != nil {
		return err
	}

	worldCtx, stopWorld := context.WithCancel(context.Background())
	defer stopWorld()
	go w.Run(worldCtx)

	server := &http.Server{
		Addr:              addr,
		Handler:           api.New(w),
		ReadHeaderTimeout: 5 * time.Second,
	}

	stopSignal := make(chan os.Signal, 1)
	signal.Notify(stopSignal, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopSignal)
	go func() {
		<-stopSignal
		stopWorld()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("jwars listening on %s (development player %s)", addr, playerID)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func waitForPostgres(ctx context.Context, pool *pgxpool.Pool) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastError error
	for {
		if err := pool.Ping(ctx); err == nil {
			return nil
		} else {
			lastError = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("connect to PostgreSQL: %w (last error: %v)", ctx.Err(), lastError)
		case <-ticker.C:
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

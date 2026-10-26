package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jojo/jwars/internal/api"
	"github.com/jojo/jwars/internal/database"
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
	if strings.EqualFold(env("JWARS_SEED_DEV_PLAYER", "true"), "false") {
		playerID, token = "", ""
	}
	addr := env("JWARS_ADDR", "127.0.0.1:8080")
	variablesDirectory := env("JWARS_VARIABLES_DIR", "variables")

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelConnect()
	pool, err := database.NewPool(connectCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := waitForPostgres(connectCtx, pool); err != nil {
		return err
	}

	w, err := world.New(pool, variablesDirectory)
	if err != nil {
		return err
	}
	if err := w.Initialize(connectCtx, playerID, token); err != nil {
		return err
	}
	adminPassword, adminCreated, err := w.AccountStore().EnsureInitialAdmin(connectCtx)
	if err != nil {
		return fmt.Errorf("initialize administrator account: %w", err)
	}
	if adminCreated {
		log.Printf("INITIAL ADMIN CREATED: username=admin temporary_password=%s; sign in to the player portal and change it immediately", adminPassword)
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

	if playerID == "" {
		log.Printf("jwars listening on %s", addr)
	} else {
		log.Printf("jwars listening on %s (development player %s)", addr, playerID)
	}
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

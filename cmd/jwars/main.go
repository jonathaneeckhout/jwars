package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jojo/jwars/internal/api"
	"github.com/jojo/jwars/internal/world"
)

func main() {
	dataPath := env("JWARS_DATA_FILE", filepath.Join("data", "world.json"))
	playerID := env("JWARS_PLAYER_ID", "player-1")
	token := env("JWARS_API_TOKEN", "dev-token")
	addr := env("JWARS_ADDR", "127.0.0.1:8080")

	w, err := world.Open(dataPath)
	if err != nil {
		log.Fatalf("open world: %v", err)
	}
	if err := w.EnsureDemoWorld(playerID); err != nil {
		log.Fatalf("initialize world: %v", err)
	}
	stopWorld := make(chan struct{})
	go w.Run(stopWorld)

	server := &http.Server{
		Addr:              addr,
		Handler:           api.New(w, playerID, token),
		ReadHeaderTimeout: 5 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		close(stopWorld)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("jwars listening on %s (player %s)", addr, playerID)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

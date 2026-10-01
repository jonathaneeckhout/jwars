package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jojo/jwars/internal/world"
)

type playerContextKey struct{}

type Server struct {
	world *world.World
}

func New(w *world.World) http.Handler {
	s := &Server{world: w}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /v1/world", s.auth(http.HandlerFunc(s.getWorld)))
	mux.Handle("GET /v1/scoreboard", s.auth(http.HandlerFunc(s.scoreboard)))
	mux.Handle("GET /v1/definitions/buildings", s.auth(http.HandlerFunc(s.getBuildingDefinitions)))
	mux.Handle("GET /v1/events", s.auth(http.HandlerFunc(s.events)))
	mux.Handle("POST /v1/commands", s.auth(http.HandlerFunc(s.commands)))
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		playerID, err := s.world.PlayerForToken(r.Context(), strings.TrimPrefix(header, prefix))
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "authentication service unavailable"})
			return
		}
		ctx := context.WithValue(r.Context(), playerContextKey{}, playerID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func playerID(r *http.Request) string {
	value, _ := r.Context().Value(playerContextKey{}).(string)
	return value
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getWorld(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.world.Snapshot(r.Context(), playerID(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load world state"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) scoreboard(w http.ResponseWriter, r *http.Request) {
	board, err := s.world.Scoreboard(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load scoreboard"})
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (s *Server) getBuildingDefinitions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"buildings": s.world.BuildingDefinitions()})
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var command world.Command
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON command"})
		return
	}
	result, err := s.world.ApplyCommand(r.Context(), playerID(r), command)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not persist command"})
		return
	}
	status := http.StatusAccepted
	if !result.Accepted {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, result)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sequence, err := parseCursor(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "after must be a non-negative sequence number"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	lastKeepAlive := time.Now()
	ownerID := playerID(r)

	for {
		updates, latest, oldest, err := s.world.EventsAfter(r.Context(), ownerID, sequence)
		if err != nil {
			return
		}
		if sequence > latest || (oldest > 0 && sequence < oldest-1) {
			snapshot, err := s.world.Snapshot(r.Context(), ownerID)
			if err != nil {
				return
			}
			if err := writeEvent(w, flusher, snapshot.Sequence, "snapshot", snapshot); err != nil {
				return
			}
			sequence = snapshot.Sequence
			continue
		}
		for _, update := range updates {
			if err := writeEvent(w, flusher, update.Sequence, update.Type, update); err != nil {
				return
			}
			sequence = update.Sequence
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			if len(updates) == 0 && time.Since(lastKeepAlive) >= 15*time.Second {
				_, _ = fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
				lastKeepAlive = time.Now()
			}
		}
	}
}

func parseCursor(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("after")
	if value == "" {
		value = r.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 {
		return 0, strconv.ErrRange
	}
	return sequence, nil
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, sequence int64, kind string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", sequence, kind, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

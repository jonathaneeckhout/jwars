package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jojo/jwars/internal/world"
)

type Server struct {
	world    *world.World
	playerID string
	token    string
}

func New(w *world.World, playerID, token string) http.Handler {
	s := &Server{world: w, playerID: playerID, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /v1/world", s.auth(http.HandlerFunc(s.getWorld)))
	mux.Handle("GET /v1/events", s.auth(http.HandlerFunc(s.events)))
	mux.Handle("POST /v1/commands", s.auth(http.HandlerFunc(s.commands)))
	return mux
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.Header.Get("Authorization") != "Bearer "+s.token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getWorld(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.world.Snapshot(s.playerID))
}

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var command world.MoveCommand
	if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON command"})
		return
	}
	result := s.world.ApplyMove(s.playerID, command)
	status := http.StatusAccepted
	if !result.Accepted {
		status = http.StatusUnprocessableEntity
		if strings.Contains(result.Reason, "persist") {
			status = http.StatusInternalServerError
		}
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
	deadline := time.NewTicker(250 * time.Millisecond)
	defer deadline.Stop()

	for {
		updates, latest, oldest := s.world.UpdatesAfter(s.playerID, sequence)
		if sequence > latest || (oldest != 0 && sequence+1 < oldest) {
			snapshot := s.world.Snapshot(s.playerID)
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
		case <-deadline.C:
			if len(updates) == 0 {
				_, _ = fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
			}
		}
	}
}

func parseCursor(r *http.Request) (uint64, error) {
	value := r.URL.Query().Get("after")
	if value == "" {
		value = r.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseUint(value, 10, 64)
	return sequence, err
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, sequence uint64, kind string, value any) error {
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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jojo/jwars/internal/accounts"
	"github.com/jojo/jwars/internal/world"
)

type playerContextKey struct{}
type accountContextKey struct{}

type Server struct {
	world         *world.World
	accounts      *accounts.Store
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
	passwordWork  chan struct{}
}

type loginAttempt struct {
	started time.Time
	count   int
}

func New(w *world.World) http.Handler {
	s := &Server{world: w, accounts: w.AccountStore(), loginAttempts: make(map[string]loginAttempt), passwordWork: make(chan struct{}, 4)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/seasons/current", s.currentSeason)
	mux.HandleFunc("POST /v1/accounts", s.createAccount)
	mux.HandleFunc("POST /v1/accounts/session", s.createAccountSession)
	mux.Handle("GET /v1/account", s.accountAuth(http.HandlerFunc(s.getAccount)))
	mux.Handle("PATCH /v1/account/profile", s.accountAuth(http.HandlerFunc(s.updateAccountProfile)))
	mux.Handle("POST /v1/account/password", s.accountAuth(http.HandlerFunc(s.changeAccountPassword)))
	mux.Handle("DELETE /v1/account/session", s.accountAuth(http.HandlerFunc(s.deleteAccountSession)))
	mux.Handle("POST /v1/account/join-season", s.accountAuth(http.HandlerFunc(s.joinSeasonAsAccount)))
	mux.Handle("GET /v1/account/api-tokens", s.accountAuth(http.HandlerFunc(s.listAPITokens)))
	mux.Handle("POST /v1/account/api-tokens", s.accountAuth(http.HandlerFunc(s.createAPIToken)))
	mux.Handle("DELETE /v1/account/api-tokens/{tokenID}", s.accountAuth(http.HandlerFunc(s.deleteAPIToken)))
	mux.Handle("GET /v1/admin/accounts", s.adminAuth(http.HandlerFunc(s.listAdminAccounts)))
	mux.Handle("POST /v1/admin/accounts/{playerID}/password-reset", s.adminAuth(http.HandlerFunc(s.resetAccountPassword)))
	mux.Handle("POST /v1/admin/accounts/{playerID}/suspension", s.adminAuth(http.HandlerFunc(s.setAccountSuspension)))
	mux.HandleFunc("POST /v1/players", s.registerPlayer)
	mux.Handle("POST /v1/seasons/current/join", s.auth(http.HandlerFunc(s.joinSeason)))
	mux.Handle("GET /v1/world", s.auth(http.HandlerFunc(s.getWorld)))
	mux.HandleFunc("GET /v1/scoreboard", s.scoreboard)
	mux.Handle("GET /v1/definitions/buildings", s.auth(http.HandlerFunc(s.getBuildingDefinitions)))
	mux.Handle("GET /v1/definitions/units", s.auth(http.HandlerFunc(s.getUnitDefinitions)))
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

func accountForRequest(r *http.Request) accounts.Account {
	value, _ := r.Context().Value(accountContextKey{}).(accounts.Account)
	return value
}

func (s *Server) accountAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		account, err := s.accounts.AuthenticateSession(r.Context(), strings.TrimPrefix(header, prefix))
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, accounts.ErrSuspended) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "account service unavailable"})
			return
		}
		if account.MustChangePassword && r.URL.Path != "/v1/account" && r.URL.Path != "/v1/account/password" && r.URL.Path != "/v1/account/session" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "change your temporary password before continuing"})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), accountContextKey{}, account))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminAuth(next http.Handler) http.Handler {
	return s.accountAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := accountForRequest(r)
		if !account.IsAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	return true
}

func (s *Server) acquirePasswordWork(w http.ResponseWriter) (func(), bool) {
	select {
	case s.passwordWork <- struct{}{}:
		return func() { <-s.passwordWork }, true
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "account service is busy; try again shortly"})
		return func() {}, false
	}
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	release, ok := s.acquirePasswordWork(w)
	if !ok {
		return
	}
	defer release()
	account, err := s.accounts.CreateAccount(r.Context(), request.Username, request.Password, request.DisplayName, false)
	if errors.Is(err, accounts.ErrUsernameTaken) || errors.Is(err, accounts.ErrDisplayNameTaken) || errors.Is(err, accounts.ErrInvalidAccount) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create account"})
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) createAccountSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	allowed, retryAfter := s.allowLoginAttempt(r, request.Username)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many sign-in attempts; try again later"})
		return
	}
	release, ok := s.acquirePasswordWork(w)
	if !ok {
		return
	}
	defer release()
	session, err := s.accounts.Login(r.Context(), request.Username, request.Password)
	if errors.Is(err, accounts.ErrInvalidCredentials) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "username or password is incorrect"})
		return
	}
	if errors.Is(err, accounts.ErrSuspended) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "account is suspended"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not sign in"})
		return
	}
	s.clearLoginAttempts(r, request.Username)
	writeJSON(w, http.StatusCreated, session)
}

func loginAttemptKey(r *http.Request, username string) string {
	host := r.RemoteAddr
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.ToLower(strings.TrimSpace(username)) + "|" + host
}

func (s *Server) allowLoginAttempt(r *http.Request, username string) (bool, time.Duration) {
	key := loginAttemptKey(r, username)
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt, ok := s.loginAttempts[key]
	if !ok || now.Sub(attempt.started) >= 15*time.Minute {
		attempt = loginAttempt{started: now}
	}
	if attempt.count >= 10 {
		return false, 15*time.Minute - now.Sub(attempt.started)
	}
	attempt.count++
	s.loginAttempts[key] = attempt
	if len(s.loginAttempts) > 4096 {
		for candidate, value := range s.loginAttempts {
			if now.Sub(value.started) >= 15*time.Minute {
				delete(s.loginAttempts, candidate)
			}
		}
	}
	return true, 0
}

func (s *Server) clearLoginAttempts(r *http.Request, username string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, loginAttemptKey(r, username))
	s.loginMu.Unlock()
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, accountForRequest(r))
}

func (s *Server) updateAccountProfile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName string `json:"display_name"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	account, err := s.accounts.UpdateDisplayName(r.Context(), accountForRequest(r).PlayerID, request.DisplayName)
	if errors.Is(err, accounts.ErrDisplayNameTaken) || errors.Is(err, accounts.ErrInvalidAccount) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update account"})
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) changeAccountPassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	release, ok := s.acquirePasswordWork(w)
	if !ok {
		return
	}
	defer release()
	account := accountForRequest(r)
	if account.MustChangePassword {
		request.CurrentPassword = ""
	} else if request.CurrentPassword == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is required"})
		return
	}
	err := s.accounts.ChangePassword(r.Context(), account.PlayerID, request.CurrentPassword, request.NewPassword)
	if errors.Is(err, accounts.ErrInvalidAccount) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "password must be between 10 and 128 characters"})
		return
	}
	if errors.Is(err, accounts.ErrInvalidCredentials) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not change password"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password updated; sign in again"})
}

func (s *Server) deleteAccountSession(w http.ResponseWriter, r *http.Request) {
	const prefix = "Bearer "
	if err := s.accounts.Logout(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), prefix)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not sign out"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) joinSeasonAsAccount(w http.ResponseWriter, r *http.Request) {
	joined, err := s.world.JoinSeason(r.Context(), accountForRequest(r).PlayerID)
	if errors.Is(err, world.ErrEnrollmentClosed) || errors.Is(err, world.ErrSeasonFull) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not join current season"})
		return
	}
	writeJSON(w, http.StatusOK, joined)
}

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.accounts.ListAPITokens(r.Context(), accountForRequest(r).PlayerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load API tokens"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	token, err := s.accounts.CreateAPIToken(r.Context(), accountForRequest(r).PlayerID, request.Name)
	if errors.Is(err, accounts.ErrInvalidAccount) || errors.Is(err, accounts.ErrTooManyTokens) || errors.Is(err, accounts.ErrTokenNameTaken) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create API token"})
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	err := s.accounts.RevokeAPIToken(r.Context(), accountForRequest(r).PlayerID, r.PathValue("tokenID"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "API token not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke API token"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAdminAccounts(w http.ResponseWriter, r *http.Request) {
	items, err := s.accounts.ListAdminAccounts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load accounts"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": items})
}

func (s *Server) resetAccountPassword(w http.ResponseWriter, r *http.Request) {
	release, ok := s.acquirePasswordWork(w)
	if !ok {
		return
	}
	defer release()
	password, err := s.accounts.ResetPassword(r.Context(), r.PathValue("playerID"))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "account not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not reset password"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"temporary_password": password, "must_change_password": true})
}

func (s *Server) setAccountSuspension(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Suspended bool `json:"suspended"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if r.PathValue("playerID") == accountForRequest(r).PlayerID && request.Suspended {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "admins cannot suspend their own account"})
		return
	}
	err := s.accounts.SetSuspended(r.Context(), r.PathValue("playerID"), request.Suspended)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "account not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update account status"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"suspended": request.Suspended})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) currentSeason(w http.ResponseWriter, r *http.Request) {
	status, err := s.world.CurrentSeason(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load current season"})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) registerPlayer(w http.ResponseWriter, r *http.Request) {
	registration, err := s.world.RegisterPlayer(r.Context())
	if errors.Is(err, world.ErrEnrollmentClosed) || errors.Is(err, world.ErrSeasonFull) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not register player"})
		return
	}
	writeJSON(w, http.StatusCreated, registration)
}

func (s *Server) joinSeason(w http.ResponseWriter, r *http.Request) {
	joined, err := s.world.JoinSeason(r.Context(), playerID(r))
	if errors.Is(err, world.ErrEnrollmentClosed) || errors.Is(err, world.ErrSeasonFull) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not join current season"})
		return
	}
	writeJSON(w, http.StatusOK, joined)
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

func (s *Server) getUnitDefinitions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"units": s.world.UnitDefinitions()})
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

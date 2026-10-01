package world

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS world_meta (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		tick BIGINT NOT NULL DEFAULT 0 CHECK (tick >= 0)
	)`,
	`CREATE TABLE IF NOT EXISTS world_season (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		season_number BIGINT NOT NULL CHECK (season_number > 0),
		starts_at TIMESTAMPTZ NOT NULL,
		ends_at TIMESTAMPTZ NOT NULL,
		status TEXT NOT NULL CHECK (status = 'active')
	)`,
	`CREATE TABLE IF NOT EXISTS hill_state (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		x INTEGER NOT NULL CHECK (x >= 0 AND x < 1000),
		y INTEGER NOT NULL CHECK (y >= 0 AND y < 1000),
		control_radius INTEGER NOT NULL CHECK (control_radius >= 0),
		owner_player_id TEXT,
		contested BOOLEAN NOT NULL DEFAULT FALSE
	)`,
	`CREATE TABLE IF NOT EXISTS player_scores (
		season_number BIGINT NOT NULL,
		player_id TEXT NOT NULL,
		control_ticks BIGINT NOT NULL DEFAULT 0 CHECK (control_ticks >= 0),
		PRIMARY KEY (season_number, player_id)
	)`,
	`CREATE TABLE IF NOT EXISTS season_history (
		season_number BIGINT PRIMARY KEY,
		result JSONB NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS players (
		player_id TEXT PRIMARY KEY,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS api_tokens (
		token_hash BYTEA PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(player_id),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS api_tokens_player_id_idx ON api_tokens(player_id)`,
	`CREATE TABLE IF NOT EXISTS player_resources (
		player_id TEXT PRIMARY KEY REFERENCES players(player_id),
		materials BIGINT NOT NULL DEFAULT 100 CHECK (materials >= 0)
	)`,
	`CREATE TABLE IF NOT EXISTS units (
		id TEXT PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(player_id),
		kind TEXT NOT NULL,
		x INTEGER NOT NULL CHECK (x >= 0 AND x < 1000),
		y INTEGER NOT NULL CHECK (y >= 0 AND y < 1000),
		health INTEGER NOT NULL DEFAULT 100 CHECK (health > 0),
		target_x INTEGER CHECK (target_x >= 0 AND target_x < 1000),
		target_y INTEGER CHECK (target_y >= 0 AND target_y < 1000),
		target_unit_id TEXT REFERENCES units(id) ON DELETE SET NULL,
		attack_target_x INTEGER CHECK (attack_target_x >= 0 AND attack_target_x < 1000),
		attack_target_y INTEGER CHECK (attack_target_y >= 0 AND attack_target_y < 1000),
		CHECK ((target_x IS NULL) = (target_y IS NULL)),
		CHECK ((attack_target_x IS NULL) = (attack_target_y IS NULL)),
		CHECK (target_unit_id IS NULL OR (attack_target_x IS NOT NULL AND attack_target_y IS NOT NULL)),
		CHECK (target_unit_id IS NULL OR (target_x IS NULL AND target_y IS NULL))
	)`,
	`CREATE INDEX IF NOT EXISTS units_player_id_idx ON units(player_id, id)`,
	`CREATE INDEX IF NOT EXISTS units_target_idx ON units(id) WHERE target_x IS NOT NULL`,
	`CREATE TABLE IF NOT EXISTS buildings (
		id TEXT PRIMARY KEY,
		player_id TEXT NOT NULL REFERENCES players(player_id),
		kind TEXT NOT NULL,
		x INTEGER NOT NULL CHECK (x >= 0 AND x < 1000),
		y INTEGER NOT NULL CHECK (y >= 0 AND y < 1000),
		width INTEGER NOT NULL CHECK (width > 0),
		height INTEGER NOT NULL CHECK (height > 0),
		status TEXT NOT NULL CHECK (status IN ('constructing', 'complete')),
		started_tick BIGINT NOT NULL DEFAULT 0 CHECK (started_tick >= 0),
		build_ticks BIGINT NOT NULL DEFAULT 0 CHECK (build_ticks >= 0),
		completion_tick BIGINT,
		CHECK ((status = 'constructing' AND completion_tick IS NOT NULL AND completion_tick = started_tick + build_ticks AND build_ticks > 0) OR (status = 'complete' AND completion_tick IS NULL)),
		CHECK (x + width <= 1000 AND y + height <= 1000)
	)`,
	`CREATE INDEX IF NOT EXISTS buildings_player_id_idx ON buildings(player_id, id)`,
	`CREATE INDEX IF NOT EXISTS buildings_completion_idx ON buildings(completion_tick) WHERE status = 'constructing'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS one_active_construction_per_player_idx ON buildings(player_id) WHERE status = 'constructing'`,
	`CREATE TABLE IF NOT EXISTS player_sequences (
		player_id TEXT PRIMARY KEY REFERENCES players(player_id),
		sequence BIGINT NOT NULL CHECK (sequence >= 0)
	)`,
	`CREATE TABLE IF NOT EXISTS world_events (
		player_id TEXT NOT NULL REFERENCES players(player_id),
		sequence BIGINT NOT NULL CHECK (sequence > 0),
		tick BIGINT NOT NULL CHECK (tick >= 0),
		payload JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (player_id, sequence)
	)`,
	`CREATE TABLE IF NOT EXISTS commands (
		player_id TEXT NOT NULL REFERENCES players(player_id),
		command_id TEXT NOT NULL,
		accepted BOOLEAN NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		sequence BIGINT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (player_id, command_id)
	)`,
}

func (w *World) Initialize(ctx context.Context, playerID, token string) error {
	for _, statement := range schemaStatements {
		if _, err := w.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("initialize schema: %w", err)
		}
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO world_meta (singleton, tick) VALUES (TRUE, 0) ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return fmt.Errorf("initialize world clock: %w", err)
	}
	now := w.now()
	if _, err := w.pool.Exec(ctx, `INSERT INTO world_season (singleton,season_number,starts_at,ends_at,status) VALUES (TRUE,1,$1,$2,'active') ON CONFLICT (singleton) DO NOTHING`, now, now.Add(w.seasonDuration)); err != nil {
		return fmt.Errorf("initialize season: %w", err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO hill_state (singleton,x,y,control_radius) VALUES (TRUE,$1,$2,$3) ON CONFLICT (singleton) DO NOTHING`, w.hill.X, w.hill.Y, w.hillRadius); err != nil {
		return fmt.Errorf("initialize hill: %w", err)
	}
	if playerID == "" || token == "" {
		return errors.New("development player id and API token must be non-empty")
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO players (player_id) VALUES ($1) ON CONFLICT (player_id) DO NOTHING`, playerID); err != nil {
		return err
	}
	var seasonNumber int64
	if err := tx.QueryRow(ctx, `SELECT season_number FROM world_season WHERE singleton=TRUE`).Scan(&seasonNumber); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_scores (season_number,player_id,control_ticks) VALUES ($1,$2,0) ON CONFLICT DO NOTHING`, seasonNumber, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_resources (player_id, materials) VALUES ($1, 100) ON CONFLICT (player_id) DO NOTHING`, playerID); err != nil {
		return err
	}
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := tx.Exec(ctx, `DELETE FROM api_tokens WHERE player_id = $1`, playerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (token_hash, player_id) VALUES ($1, $2) ON CONFLICT (token_hash) DO NOTHING`, tokenHash[:], playerID); err != nil {
		return err
	}
	var tokenOwner string
	if err := tx.QueryRow(ctx, `SELECT player_id FROM api_tokens WHERE token_hash = $1`, tokenHash[:]).Scan(&tokenOwner); err != nil {
		return err
	}
	if tokenOwner != playerID {
		return fmt.Errorf("development API token is already assigned to player %q", tokenOwner)
	}

	var unitCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM units WHERE player_id = $1`, playerID).Scan(&unitCount); err != nil {
		return err
	}
	if unitCount == 0 {
		for _, unit := range []Unit{
			{OwnerID: playerID, Kind: "worker", X: 13, Y: 12},
			{OwnerID: playerID, Kind: "soldier", X: 14, Y: 12},
		} {
			unit.ID, err = newID()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO units (id, player_id, kind, x, y) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`, unit.ID, playerID, unit.Kind, unit.X, unit.Y); err != nil {
				return err
			}
		}
	}

	var buildingCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM buildings WHERE player_id = $1`, playerID).Scan(&buildingCount); err != nil {
		return err
	}
	if buildingCount == 0 {
		for _, definition := range w.definitions {
			if !definition.Starting {
				continue
			}
			buildingID, err := newID()
			if err != nil {
				return err
			}
			building := Building{
				ID: buildingID, OwnerID: playerID,
				Kind: definition.Kind, X: definition.StartX, Y: definition.StartY,
				Width: definition.Width, Height: definition.Height, Status: "complete",
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO buildings (id, player_id, kind, x, y, width, height, status)
				VALUES ($1,$2,$3,$4,$5,$6,$7,'complete') ON CONFLICT (id) DO NOTHING`,
				building.ID, playerID, building.Kind, building.X, building.Y, building.Width, building.Height); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (w *World) PlayerForToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", pgx.ErrNoRows
	}
	tokenHash := sha256.Sum256([]byte(token))
	var playerID string
	err := w.pool.QueryRow(ctx, `SELECT player_id FROM api_tokens WHERE token_hash = $1`, tokenHash[:]).Scan(&playerID)
	return playerID, err
}

func readCommandResult(ctx context.Context, tx pgx.Tx, playerID, commandID string) (CommandResult, error) {
	var result CommandResult
	result.ID = commandID
	var sequence int64
	err := tx.QueryRow(ctx, `
		SELECT accepted, reason, COALESCE(sequence, 0)
		FROM commands WHERE player_id = $1 AND command_id = $2`, playerID, commandID).
		Scan(&result.Accepted, &result.Reason, &sequence)
	result.Sequence = sequence
	return result, err
}

func insertCommandResult(ctx context.Context, tx pgx.Tx, playerID string, result CommandResult) error {
	var sequence any
	if result.Sequence > 0 {
		sequence = result.Sequence
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO commands (player_id, command_id, accepted, reason, sequence)
		VALUES ($1,$2,$3,$4,$5)`, playerID, result.ID, result.Accepted, result.Reason, sequence)
	return err
}

func appendEvent(ctx context.Context, tx pgx.Tx, update Update) (int64, error) {
	var sequence int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO player_sequences (player_id, sequence) VALUES ($1, 1)
		ON CONFLICT (player_id) DO UPDATE SET sequence = player_sequences.sequence + 1
		RETURNING sequence`, update.PlayerID).Scan(&sequence); err != nil {
		return 0, err
	}
	update.Sequence = sequence
	payload, err := json.Marshal(update)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO world_events (player_id, sequence, tick, payload)
		VALUES ($1,$2,$3,$4)`, update.PlayerID, sequence, update.Tick, payload); err != nil {
		return 0, err
	}
	if sequence > EventLimit {
		if _, err := tx.Exec(ctx, `DELETE FROM world_events WHERE player_id = $1 AND sequence <= $2`, update.PlayerID, sequence-EventLimit); err != nil {
			return 0, err
		}
	}
	return sequence, nil
}

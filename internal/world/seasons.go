package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	DefaultSeasonDuration = 7 * 24 * time.Hour
	TicksPerControlPoint  = 60
)

type Options struct {
	Hill           Point
	HillRadius     int
	SeasonDuration time.Duration
	Now           func() time.Time
}

type SeasonInfo struct {
	Number    int64     `json:"number"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Status    string    `json:"status"`
}

type HillState struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Radius     int     `json:"control_radius"`
	OwnerID    *string `json:"owner_player_id"`
	Contested  bool    `json:"contested"`
}

type Standing struct {
	PlayerID       string `json:"player_id"`
	Score          int64  `json:"score"`
	ControlSeconds int64  `json:"control_seconds"`
}

type SeasonResult struct {
	Season    SeasonInfo `json:"season"`
	Standings []Standing `json:"standings"`
	Winners   []string   `json:"winners"`
}

type Scoreboard struct {
	Season        SeasonInfo    `json:"season"`
	Hill          HillState     `json:"hill"`
	Standings     []Standing    `json:"standings"`
	LastCompleted *SeasonResult `json:"last_completed,omitempty"`
}

func (w *World) now() time.Time { return w.clock().UTC() }

func (w *World) Scoreboard(ctx context.Context) (Scoreboard, error) {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Scoreboard{}, err
	}
	defer tx.Rollback(ctx)
	board, err := readScoreboard(ctx, tx)
	if err != nil {
		return Scoreboard{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Scoreboard{}, err
	}
	return board, nil
}

func readScoreboard(ctx context.Context, tx pgx.Tx) (Scoreboard, error) {
	var board Scoreboard
	if err := tx.QueryRow(ctx, `SELECT season_number, starts_at, ends_at, status FROM world_season WHERE singleton=TRUE`).
		Scan(&board.Season.Number, &board.Season.StartsAt, &board.Season.EndsAt, &board.Season.Status); err != nil {
		return board, err
	}
	var owner pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT x, y, control_radius, owner_player_id, contested FROM hill_state WHERE singleton=TRUE`).
		Scan(&board.Hill.X, &board.Hill.Y, &board.Hill.Radius, &owner, &board.Hill.Contested); err != nil {
		return board, err
	}
	if owner.Valid {
		board.Hill.OwnerID = &owner.String
	}
	rows, err := tx.Query(ctx, `
		SELECT p.player_id, COALESCE(s.control_ticks,0)
		FROM players p LEFT JOIN player_scores s
		ON s.player_id=p.player_id AND s.season_number=$1
		ORDER BY p.player_id`, board.Season.Number)
	if err != nil {
		return board, err
	}
	board.Standings = make([]Standing, 0)
	for rows.Next() {
		var standing Standing
		if err := rows.Scan(&standing.PlayerID, &standing.ControlSeconds); err != nil {
			rows.Close()
			return board, err
		}
		standing.Score = standing.ControlSeconds / TicksPerControlPoint
		board.Standings = append(board.Standings, standing)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return board, err
	}
	sort.Slice(board.Standings, func(i, j int) bool {
		if board.Standings[i].Score == board.Standings[j].Score {
			return board.Standings[i].PlayerID < board.Standings[j].PlayerID
		}
		return board.Standings[i].Score > board.Standings[j].Score
	})
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT result FROM season_history ORDER BY season_number DESC LIMIT 1`).Scan(&raw); err == nil {
		var result SeasonResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return board, err
		}
		board.LastCompleted = &result
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return board, err
	}
	return board, nil
}

func seasonEndIfDue(ctx context.Context, tx pgx.Tx, now time.Time) (bool, error) {
	var endsAt time.Time
	if err := tx.QueryRow(ctx, `SELECT ends_at FROM world_season WHERE singleton=TRUE FOR UPDATE`).Scan(&endsAt); err != nil {
		return false, err
	}
	return !now.Before(endsAt), nil
}

func (w *World) finishAndResetSeason(ctx context.Context, tx pgx.Tx, players []string, now time.Time) error {
	var season SeasonInfo
	if err := tx.QueryRow(ctx, `SELECT season_number, starts_at, ends_at, status FROM world_season WHERE singleton=TRUE`).
		Scan(&season.Number, &season.StartsAt, &season.EndsAt, &season.Status); err != nil {
		return err
	}
	board, err := readScoreboard(ctx, tx)
	if err != nil {
		return err
	}
	var highest int64
	for _, standing := range board.Standings {
		if standing.Score > highest {
			highest = standing.Score
		}
	}
	winners := make([]string, 0)
	for _, standing := range board.Standings {
		if standing.Score == highest {
			winners = append(winners, standing.PlayerID)
		}
	}
	result := SeasonResult{Season: season, Standings: board.Standings, Winners: winners}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO season_history (season_number,result) VALUES ($1,$2)`, season.Number, payload); err != nil {
		return err
	}
	for _, playerID := range players {
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: 0, Type: "season.ended", Season: &season, Standings: board.Standings, Winners: winners}); err != nil {
			return err
		}
	}

	next := SeasonInfo{Number: season.Number + 1, StartsAt: now, EndsAt: now.Add(w.seasonDuration), Status: "active"}
	if _, err := tx.Exec(ctx, `UPDATE world_season SET season_number=$1, starts_at=$2, ends_at=$3, status='active' WHERE singleton=TRUE`, next.Number, next.StartsAt, next.EndsAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM commands`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM units`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM buildings`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE player_resources SET materials=100`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM player_scores`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hill_state SET owner_player_id=NULL, contested=FALSE WHERE singleton=TRUE`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE world_meta SET tick=0 WHERE singleton=TRUE`); err != nil {
		return err
	}
	for _, playerID := range players {
		if _, err := tx.Exec(ctx, `INSERT INTO player_scores (season_number,player_id,control_ticks) VALUES ($1,$2,0)`, next.Number, playerID); err != nil {
			return err
		}
		if err := w.seedPlayer(ctx, tx, playerID); err != nil {
			return err
		}
	}
	newHill := HillState{X: w.hill.X, Y: w.hill.Y, Radius: w.hillRadius}
	for _, playerID := range players {
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: 0, Type: "season.started", Season: &next, Hill: &newHill}); err != nil {
			return err
		}
	}
	return nil
}

func (w *World) seedPlayer(ctx context.Context, tx pgx.Tx, playerID string) error {
	for _, unit := range []Unit{
		{OwnerID: playerID, Kind: "worker", X: 13, Y: 12},
		{OwnerID: playerID, Kind: "soldier", X: 14, Y: 12},
	} {
		unitID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO units (id,player_id,kind,x,y) VALUES ($1,$2,$3,$4,$5)`, unitID, playerID, unit.Kind, unit.X, unit.Y); err != nil {
			return err
		}
	}
	for _, definition := range w.definitions {
		if !definition.Starting {
			continue
		}
		buildingID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO buildings (id,player_id,kind,x,y,width,height,status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'complete')`,
			buildingID, playerID, definition.Kind, definition.StartX, definition.StartY, definition.Width, definition.Height); err != nil {
			return fmt.Errorf("seed starting building: %w", err)
		}
	}
	return nil
}

func (w *World) updateHillControl(ctx context.Context, tx pgx.Tx, tick int64, players []string, units []Unit) error {
	owners := make(map[string]bool)
	for _, unit := range units {
		if unit.Kind != "soldier" {
			continue
		}
		dx, dy := unit.X-w.hill.X, unit.Y-w.hill.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx <= w.hillRadius && dy <= w.hillRadius {
			owners[unit.OwnerID] = true
		}
	}
	var owner *string
	contested := len(owners) > 1
	if len(owners) == 1 {
		for id := range owners {
			value := id
			owner = &value
		}
	}
	var oldOwner pgtype.Text
	var oldContested bool
	if err := tx.QueryRow(ctx, `SELECT owner_player_id,contested FROM hill_state WHERE singleton=TRUE FOR UPDATE`).Scan(&oldOwner, &oldContested); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hill_state SET owner_player_id=$1,contested=$2 WHERE singleton=TRUE`, owner, contested); err != nil {
		return err
	}
	state := HillState{X: w.hill.X, Y: w.hill.Y, Radius: w.hillRadius, OwnerID: owner, Contested: contested}
	changed := oldOwner.Valid != (owner != nil) || oldContested != contested || (oldOwner.Valid && owner != nil && oldOwner.String != *owner)
	if changed {
		for _, playerID := range players {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "hill.changed", Hill: &state}); err != nil {
				return err
			}
		}
	}
	if owner == nil {
		return nil
	}
	var oldTicks int64
	if err := tx.QueryRow(ctx, `SELECT control_ticks FROM player_scores WHERE season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE) AND player_id=$1 FOR UPDATE`, *owner).Scan(&oldTicks); err != nil {
		return err
	}
	newTicks := oldTicks + 1
	if _, err := tx.Exec(ctx, `UPDATE player_scores SET control_ticks=$2 WHERE season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE) AND player_id=$1`, *owner, newTicks); err != nil {
		return err
	}
	if newTicks/TicksPerControlPoint > oldTicks/TicksPerControlPoint {
		score := newTicks/TicksPerControlPoint
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: *owner, Tick: tick, Type: "score.changed", Score: &score, Hill: &state}); err != nil {
			return err
		}
	}
	return nil
}

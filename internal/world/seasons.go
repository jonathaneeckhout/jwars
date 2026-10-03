package world

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Options struct {
	Hill           Point
	HillRadius     int
	SeasonDuration time.Duration
	Now            func() time.Time
}

type SeasonInfo struct {
	Number   int64     `json:"number"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Status   string    `json:"status"`
}

type SeasonStatus struct {
	SeasonInfo
	EnrollmentEndsAt time.Time `json:"enrollment_ends_at"`
	EnrollmentOpen   bool      `json:"enrollment_open"`
	Players          int       `json:"players"`
	MaxPlayers       int       `json:"max_players"`
}

type SeasonJoin struct {
	PlayerID      string     `json:"player_id"`
	Season        SeasonInfo `json:"season"`
	SpawnLocation Point      `json:"spawn_location"`
	AlreadyJoined bool       `json:"already_joined,omitempty"`
}

type PlayerRegistration struct {
	PlayerID string     `json:"player_id"`
	Token    string     `json:"token"`
	Season   SeasonJoin `json:"season"`
}

var (
	ErrEnrollmentClosed = errors.New("season enrollment is closed")
	ErrSeasonFull       = errors.New("season is full")
)

func seasonStartAtOrBefore(now time.Time, weekdayName, timeOfDay string) time.Time {
	targetDay, _ := parseWeekday(strings.ToLower(weekdayName))
	clock, _ := time.Parse("15:04", timeOfDay)
	utc := now.UTC()
	candidate := time.Date(utc.Year(), utc.Month(), utc.Day(), clock.Hour(), clock.Minute(), 0, 0, time.UTC)
	daysBack := (int(utc.Weekday()) - int(targetDay) + 7) % 7
	candidate = candidate.AddDate(0, 0, -daysBack)
	if candidate.After(utc) {
		candidate = candidate.AddDate(0, 0, -7)
	}
	return candidate
}

func seasonStartForNewWorld(now time.Time, world WorldSettings) time.Time {
	start := seasonStartAtOrBefore(now, world.SeasonStartWeekday, world.SeasonStartTimeUTC)
	enrollmentEndsAt := start.Add(time.Duration(world.EnrollmentHours) * time.Hour)
	if !now.Before(enrollmentEndsAt) {
		start = start.AddDate(0, 0, 7)
	}
	return start
}

func (w *World) seasonHasStarted(ctx context.Context, tx pgx.Tx, now time.Time) (bool, error) {
	var startsAt time.Time
	if err := tx.QueryRow(ctx, `SELECT starts_at FROM world_season WHERE singleton=TRUE`).Scan(&startsAt); err != nil {
		return false, err
	}
	return !now.Before(startsAt), nil
}

func (w *World) CurrentSeason(ctx context.Context) (SeasonStatus, error) {
	var status SeasonStatus
	if err := w.pool.QueryRow(ctx, `
		SELECT s.season_number,s.starts_at,s.ends_at,s.status,
		       (SELECT count(*) FROM season_players p WHERE p.season_number=s.season_number)
		FROM world_season s WHERE s.singleton=TRUE`).
		Scan(&status.Number, &status.StartsAt, &status.EndsAt, &status.Status, &status.Players); err != nil {
		return status, err
	}
	status.EnrollmentEndsAt = status.StartsAt.Add(time.Duration(w.config.World.EnrollmentHours) * time.Hour)
	status.MaxPlayers = w.config.World.MaxPlayers
	now := w.now()
	status.Status = seasonPhase(status.SeasonInfo, now)
	status.EnrollmentOpen = !now.Before(status.StartsAt) && now.Before(status.EnrollmentEndsAt) && status.Players < status.MaxPlayers
	return status, nil
}

func seasonPhase(season SeasonInfo, now time.Time) string {
	if now.Before(season.StartsAt) {
		return "scheduled"
	}
	if !now.Before(season.EndsAt) {
		return "ended"
	}
	return "active"
}

func (w *World) RegisterPlayer(ctx context.Context) (PlayerRegistration, error) {
	w.writer.Lock()
	defer w.writer.Unlock()
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return PlayerRegistration{}, err
	}
	defer tx.Rollback(ctx)
	season, err := lockCurrentSeason(ctx, tx)
	if err != nil {
		return PlayerRegistration{}, err
	}
	playerID, err := newID()
	if err != nil {
		return PlayerRegistration{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return PlayerRegistration{}, err
	}
	token := hex.EncodeToString(secret[:])
	if _, err := tx.Exec(ctx, `INSERT INTO players (player_id) VALUES ($1)`, playerID); err != nil {
		return PlayerRegistration{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (token_hash,player_id) VALUES ($1,$2)`, tokenHash[:], playerID); err != nil {
		return PlayerRegistration{}, err
	}
	join, err := w.joinPlayerTx(ctx, tx, playerID, season, true)
	if err != nil {
		return PlayerRegistration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlayerRegistration{}, err
	}
	return PlayerRegistration{PlayerID: playerID, Token: token, Season: join}, nil
}

func (w *World) JoinSeason(ctx context.Context, playerID string) (SeasonJoin, error) {
	w.writer.Lock()
	defer w.writer.Unlock()
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return SeasonJoin{}, err
	}
	defer tx.Rollback(ctx)
	season, err := lockCurrentSeason(ctx, tx)
	if err != nil {
		return SeasonJoin{}, err
	}
	var spawnIndex int
	err = tx.QueryRow(ctx, `SELECT spawn_index FROM season_players WHERE season_number=$1 AND player_id=$2`, season.Number, playerID).Scan(&spawnIndex)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return SeasonJoin{}, err
		}
		return SeasonJoin{PlayerID: playerID, Season: season, SpawnLocation: w.config.World.SpawnLocations[spawnIndex], AlreadyJoined: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SeasonJoin{}, err
	}
	join, err := w.joinPlayerTx(ctx, tx, playerID, season, true)
	if err != nil {
		return SeasonJoin{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SeasonJoin{}, err
	}
	return join, nil
}

func lockCurrentSeason(ctx context.Context, tx pgx.Tx) (SeasonInfo, error) {
	var season SeasonInfo
	err := tx.QueryRow(ctx, `SELECT season_number,starts_at,ends_at,status FROM world_season WHERE singleton=TRUE FOR UPDATE`).
		Scan(&season.Number, &season.StartsAt, &season.EndsAt, &season.Status)
	return season, err
}

func (w *World) joinPlayerTx(ctx context.Context, tx pgx.Tx, playerID string, season SeasonInfo, enforceEnrollment bool) (SeasonJoin, error) {
	status := SeasonStatus{SeasonInfo: season}
	status.EnrollmentEndsAt = season.StartsAt.Add(time.Duration(w.config.World.EnrollmentHours) * time.Hour)
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM season_players WHERE season_number=$1`, season.Number).Scan(&count); err != nil {
		return SeasonJoin{}, err
	}
	if enforceEnrollment {
		now := w.now()
		if now.Before(season.StartsAt) || !now.Before(status.EnrollmentEndsAt) {
			return SeasonJoin{}, ErrEnrollmentClosed
		}
		if count >= w.config.World.MaxPlayers {
			return SeasonJoin{}, ErrSeasonFull
		}
	}
	spawnIndex, err := w.nextSpawnIndex(ctx, tx, season.Number, playerID)
	if err != nil {
		return SeasonJoin{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO season_players (season_number,player_id,spawn_index,joined_at) VALUES ($1,$2,$3,$4)`, season.Number, playerID, spawnIndex, w.now()); err != nil {
		return SeasonJoin{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_scores (season_number,player_id,control_ticks) VALUES ($1,$2,0) ON CONFLICT DO NOTHING`, season.Number, playerID); err != nil {
		return SeasonJoin{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO player_resources (player_id,materials) VALUES ($1,$2) ON CONFLICT (player_id) DO UPDATE SET materials=EXCLUDED.materials`, playerID, w.config.Economy.StartingMaterials); err != nil {
		return SeasonJoin{}, err
	}
	spawn := w.config.World.SpawnLocations[spawnIndex]
	if err := w.seedPlayer(ctx, tx, playerID, spawn); err != nil {
		return SeasonJoin{}, err
	}
	var tick int64
	if err := tx.QueryRow(ctx, `SELECT tick FROM world_meta WHERE singleton=TRUE`).Scan(&tick); err != nil {
		return SeasonJoin{}, err
	}
	var hill HillState
	var owner pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT x,y,control_radius,owner_player_id,contested FROM hill_state WHERE singleton=TRUE`).
		Scan(&hill.X, &hill.Y, &hill.Radius, &owner, &hill.Contested); err != nil {
		return SeasonJoin{}, err
	}
	if owner.Valid {
		hill.OwnerID = &owner.String
	}
	if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "season.joined", Season: &season, Hill: &hill}); err != nil {
		return SeasonJoin{}, err
	}
	return SeasonJoin{PlayerID: playerID, Season: season, SpawnLocation: spawn}, nil
}

type HillState struct {
	X         int     `json:"x"`
	Y         int     `json:"y"`
	Radius    int     `json:"control_radius"`
	OwnerID   *string `json:"owner_player_id"`
	Contested bool    `json:"contested"`
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
	board, err := readScoreboard(ctx, tx, w.config.World.TicksPerControlPoint)
	if err != nil {
		return Scoreboard{}, err
	}
	board.Season.Status = seasonPhase(board.Season, w.now())
	if err := tx.Commit(ctx); err != nil {
		return Scoreboard{}, err
	}
	return board, nil
}

func readScoreboard(ctx context.Context, tx pgx.Tx, ticksPerControlPoint int64) (Scoreboard, error) {
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
		FROM season_players p LEFT JOIN player_scores s
		ON s.player_id=p.player_id AND s.season_number=$1
		WHERE p.season_number=$1
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
		standing.Score = standing.ControlSeconds / ticksPerControlPoint
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
	board, err := readScoreboard(ctx, tx, w.config.World.TicksPerControlPoint)
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

	nextStart := seasonStartAtOrBefore(season.EndsAt, w.config.World.SeasonStartWeekday, w.config.World.SeasonStartTimeUTC)
	if nextStart.Before(season.EndsAt) {
		nextStart = nextStart.AddDate(0, 0, 7)
	}
	if nextStart.After(now) {
		return errors.New("season reset requested before season end")
	}
	next := SeasonInfo{Number: season.Number + 1, StartsAt: nextStart, EndsAt: nextStart.Add(w.seasonDuration), Status: "active"}
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
	if _, err := tx.Exec(ctx, `UPDATE player_resources SET materials=$1`, w.config.Economy.StartingMaterials); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE resource_deposits SET amount=capacity`); err != nil {
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
	return nil
}

func (w *World) seedPlayer(ctx context.Context, tx pgx.Tx, playerID string, spawn Point) error {
	if err := w.seedStartingUnits(ctx, tx, playerID, spawn); err != nil {
		return err
	}
	return w.seedStartingBuildings(ctx, tx, playerID, spawn)
}

func (w *World) seedStartingUnits(ctx context.Context, tx pgx.Tx, playerID string, spawn Point) error {
	for _, startingUnit := range w.config.World.StartingUnits {
		unit := Unit{OwnerID: playerID, Kind: startingUnit.Kind, X: spawn.X + startingUnit.X, Y: spawn.Y + startingUnit.Y}
		unitID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO units (id,player_id,kind,x,y,health) VALUES ($1,$2,$3,$4,$5,$6)`, unitID, playerID, unit.Kind, unit.X, unit.Y, w.unitMaxHealth(unit.Kind)); err != nil {
			return err
		}
	}
	return nil
}

func (w *World) seedStartingBuildings(ctx context.Context, tx pgx.Tx, playerID string, spawn Point) error {
	for _, definition := range w.definitions {
		if !definition.Starting {
			continue
		}
		buildingID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO buildings (id,player_id,kind,x,y,width,height,status,health)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'complete',$8)`,
			buildingID, playerID, definition.Kind, spawn.X+definition.StartX, spawn.Y+definition.StartY, definition.Width, definition.Height, definition.MaxHealth); err != nil {
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
	if newTicks/w.config.World.TicksPerControlPoint > oldTicks/w.config.World.TicksPerControlPoint {
		score := newTicks / w.config.World.TicksPerControlPoint
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: *owner, Tick: tick, Type: "score.changed", Score: &score, Hill: &state}); err != nil {
			return err
		}
	}
	return nil
}

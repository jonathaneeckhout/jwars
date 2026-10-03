package world

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const EventLimit = 2000

type Unit struct {
	ID                     string  `json:"id"`
	OwnerID                string  `json:"owner_id"`
	Kind                   string  `json:"kind"`
	X                      int     `json:"x"`
	Y                      int     `json:"y"`
	Health                 int     `json:"health"`
	MaxHealth              int     `json:"max_health"`
	VisionRange            int     `json:"vision_range"`
	TargetX                *int    `json:"target_x,omitempty"`
	TargetY                *int    `json:"target_y,omitempty"`
	AttackTargetID         *string `json:"attack_target_id,omitempty"`
	AttackTargetBuildingID *string `json:"attack_target_building_id,omitempty"`
	AttackTargetX          *int    `json:"-"`
	AttackTargetY          *int    `json:"-"`
	GatherTargetID         *string `json:"gather_target_id,omitempty"`
	GatherProgress         int     `json:"-"`
}

type Building struct {
	ID             string `json:"id"`
	OwnerID        string `json:"owner_id"`
	Kind           string `json:"kind"`
	X              int    `json:"x"`
	Y              int    `json:"y"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Status         string `json:"status"`
	Destructible   bool   `json:"destructible"`
	Health         int    `json:"health"`
	MaxHealth      int    `json:"max_health"`
	BuildTicks     int64  `json:"build_ticks"`
	ProgressTicks  int64  `json:"progress_ticks"`
	ProgressPct    int    `json:"progress_percent"`
	VisionRange    int    `json:"vision_range"`
	StartedTick    int64  `json:"-"`
	CompletionTick *int64 `json:"completion_tick,omitempty"`
}

type Resource struct {
	Kind   string `json:"kind"`
	Amount int64  `json:"amount"`
}

type ResourceDeposit struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Amount   int    `json:"amount"`
	Capacity int    `json:"capacity"`
}

type TrainingOrder struct {
	ID             string `json:"id"`
	BuildingID     string `json:"building_id"`
	UnitKind       string `json:"unit_kind"`
	StartedTick    int64  `json:"started_tick"`
	CompletionTick int64  `json:"completion_tick"`
	ProgressPct    int    `json:"progress_percent"`
}

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Command struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	UnitIDs          []string `json:"unit_ids,omitempty"`
	Target           *Point   `json:"target,omitempty"`
	TargetUnitID     string   `json:"target_unit_id,omitempty"`
	TargetBuildingID string   `json:"target_building_id,omitempty"`
	BuildingKind     string   `json:"building_kind,omitempty"`
	X                *int     `json:"x,omitempty"`
	Y                *int     `json:"y,omitempty"`
	BuildingID       string   `json:"building_id,omitempty"`
	UnitKind         string   `json:"unit_kind,omitempty"`
	DepositID        string   `json:"deposit_id,omitempty"`
}

type CommandResult struct {
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type Update struct {
	PlayerID  string            `json:"player_id"`
	Sequence  int64             `json:"sequence"`
	Tick      int64             `json:"tick"`
	Type      string            `json:"type"`
	Units     []Unit            `json:"units,omitempty"`
	Buildings []Building        `json:"buildings,omitempty"`
	Resources []Resource        `json:"resources,omitempty"`
	Entities  []EntityRef       `json:"entities,omitempty"`
	Score     *int64            `json:"score,omitempty"`
	Season    *SeasonInfo       `json:"season,omitempty"`
	Hill      *HillState        `json:"hill,omitempty"`
	Standings []Standing        `json:"standings,omitempty"`
	Winners   []string          `json:"winners,omitempty"`
	Deposits  []ResourceDeposit `json:"deposits,omitempty"`
	Training  []TrainingOrder   `json:"training,omitempty"`
}

type EntityRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Snapshot struct {
	PlayerID  string            `json:"player_id"`
	Joined    bool              `json:"joined"`
	Tick      int64             `json:"tick"`
	Sequence  int64             `json:"sequence"`
	Units     []Unit            `json:"units"`
	Buildings []Building        `json:"buildings"`
	Resources []Resource        `json:"resources"`
	Season    SeasonInfo        `json:"season"`
	Hill      HillState         `json:"hill"`
	Score     int64             `json:"score"`
	Deposits  []ResourceDeposit `json:"deposits"`
	Training  []TrainingOrder   `json:"training"`
}

type BuildingDefinition struct {
	Kind                string `json:"kind"`
	Destructible        bool   `json:"destructible"`
	MaxHealth           int    `json:"max_health"`
	Width               int    `json:"width"`
	Height              int    `json:"height"`
	Cost                int64  `json:"cost"`
	BuildTicks          int64  `json:"build_ticks"`
	Starting            bool   `json:"starting,omitempty"`
	StartX              int    `json:"start_x,omitempty"`
	StartY              int    `json:"start_y,omitempty"`
	VisionRange         int    `json:"vision_range"`
	AttackDamage        int    `json:"attack_damage"`
	AttackRange         int    `json:"attack_range"`
	AttackIntervalTicks int64  `json:"attack_interval_ticks"`
}

type World struct {
	pool            *pgxpool.Pool
	config          GameConfig
	definitions     map[string]BuildingDefinition
	unitDefinitions map[string]UnitDefinition
	writer          sync.Mutex
	hill            Point
	hillRadius      int
	seasonDuration  time.Duration
	tickInterval    time.Duration
	clock           func() time.Time
}

func New(pool *pgxpool.Pool, variablesDirectory string) (*World, error) {
	return NewWithOptions(pool, variablesDirectory, Options{})
}

func NewWithOptions(pool *pgxpool.Pool, variablesDirectory string, options Options) (*World, error) {
	config, err := LoadConfig(variablesDirectory)
	if err != nil {
		return nil, err
	}
	if options.Hill != (Point{}) {
		config.World.Hill = options.Hill
	}
	if options.HillRadius != 0 {
		config.World.HillRadius = options.HillRadius
	}
	if options.SeasonDuration > 0 {
		config.World.SeasonDurationSeconds = int64(options.SeasonDuration.Seconds())
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	definitions := make(map[string]BuildingDefinition, len(config.Buildings))
	for _, definition := range config.Buildings {
		definitions[definition.Kind] = definition
	}
	unitDefinitions := make(map[string]UnitDefinition, len(config.Units))
	for _, definition := range config.Units {
		unitDefinitions[definition.Kind] = definition
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &World{
		pool: pool, config: config, definitions: definitions, unitDefinitions: unitDefinitions,
		hill: config.World.Hill, hillRadius: config.World.HillRadius,
		seasonDuration: time.Duration(config.World.SeasonDurationSeconds) * time.Second,
		tickInterval:   time.Duration(config.World.TickIntervalMillis) * time.Millisecond,
		clock:          options.Now,
	}, nil
}

func (w *World) BuildingDefinitions() []BuildingDefinition {
	definitions := make([]BuildingDefinition, 0, len(w.definitions))
	for _, definition := range w.definitions {
		if !definition.Starting {
			definitions = append(definitions, definition)
		}
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Kind < definitions[j].Kind })
	return definitions
}

func (w *World) UnitDefinitions() []UnitDefinition {
	definitions := make([]UnitDefinition, 0, len(w.unitDefinitions))
	for _, definition := range w.unitDefinitions {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Kind < definitions[j].Kind })
	return definitions
}

func (w *World) Snapshot(ctx context.Context, playerID string) (Snapshot, error) {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback(ctx)

	var snapshot Snapshot
	snapshot.PlayerID = playerID
	if err := tx.QueryRow(ctx, `SELECT tick FROM world_meta WHERE singleton = TRUE`).Scan(&snapshot.Tick); err != nil {
		return Snapshot{}, err
	}
	board, err := readScoreboard(ctx, tx, w.config.World.TicksPerControlPoint)
	if err != nil {
		return Snapshot{}, err
	}
	board.Season.Status = seasonPhase(board.Season, w.now())
	snapshot.Season, snapshot.Hill = board.Season, board.Hill
	for _, standing := range board.Standings {
		if standing.PlayerID == playerID {
			snapshot.Score = standing.Score
			break
		}
	}
	var joined bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM season_players WHERE season_number=$1 AND player_id=$2)`, board.Season.Number, playerID).Scan(&joined); err != nil {
		return Snapshot{}, err
	}
	snapshot.Joined = joined
	if err := tx.QueryRow(ctx, `SELECT sequence FROM player_sequences WHERE player_id = $1`, playerID).Scan(&snapshot.Sequence); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, err
		}
	}
	if !joined {
		snapshot.Units = []Unit{}
		snapshot.Buildings = []Building{}
		snapshot.Resources = []Resource{{Kind: "materials", Amount: 0}}
		snapshot.Deposits = []ResourceDeposit{}
		snapshot.Training = []TrainingOrder{}
		if err := tx.Commit(ctx); err != nil {
			return Snapshot{}, err
		}
		return snapshot, nil
	}
	snapshot.Deposits, err = w.loadVisibleDeposits(ctx, tx, playerID)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Training, err = loadTrainingOrders(ctx, tx, playerID, snapshot.Tick)
	if err != nil {
		return Snapshot{}, err
	}

	units := make([]Unit, 0)
	buildings := make([]Building, 0)
	unitRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, target_x, target_y, health, target_unit_id, target_building_id, attack_target_x, attack_target_y, gather_target_id, gather_progress
		FROM units ORDER BY id`)
	if err != nil {
		return Snapshot{}, err
	}
	for unitRows.Next() {
		unit, err := scanUnit(unitRows)
		if err != nil {
			unitRows.Close()
			return Snapshot{}, err
		}
		units = append(units, unit)
	}
	unitRows.Close()
	if err := unitRows.Err(); err != nil {
		return Snapshot{}, err
	}
	buildingRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, width, height, status, started_tick, build_ticks, completion_tick, health
		FROM buildings ORDER BY id`)
	if err != nil {
		return Snapshot{}, err
	}
	for buildingRows.Next() {
		building, err := scanBuilding(buildingRows)
		if err != nil {
			buildingRows.Close()
			return Snapshot{}, err
		}
		setBuildingProgress(&building, snapshot.Tick)
		buildings = append(buildings, building)
	}
	buildingRows.Close()
	if err := buildingRows.Err(); err != nil {
		return Snapshot{}, err
	}
	visible := w.visibleEntities(playerID, units, buildings)
	for _, unit := range units {
		if visible[entityKey("unit:"+unit.ID)] {
			snapshot.Units = append(snapshot.Units, w.enrichUnit(unit, playerID))
		}
	}
	for _, building := range buildings {
		if visible[entityKey("building:"+building.ID)] {
			snapshot.Buildings = append(snapshot.Buildings, w.enrichBuilding(building))
		}
	}
	if snapshot.Units == nil {
		snapshot.Units = []Unit{}
	}
	if snapshot.Buildings == nil {
		snapshot.Buildings = []Building{}
	}

	var amount int64
	if err := tx.QueryRow(ctx, `SELECT materials FROM player_resources WHERE player_id = $1`, playerID).Scan(&amount); err != nil {
		return Snapshot{}, err
	}
	snapshot.Resources = []Resource{{Kind: "materials", Amount: amount}}
	if err := tx.Commit(ctx); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (w *World) EventsAfter(ctx context.Context, playerID string, sequence int64) (updates []Update, latest int64, oldest int64, err error) {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, 0, err
	}
	defer tx.Rollback(ctx)

	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence), 0), COALESCE(MIN(sequence), 0) FROM world_events WHERE player_id = $1`, playerID).Scan(&latest, &oldest); err != nil {
		return nil, 0, 0, err
	}
	rows, err := tx.Query(ctx, `
		SELECT payload FROM world_events
		WHERE player_id = $1 AND sequence > $2 ORDER BY sequence`, playerID, sequence)
	if err != nil {
		return nil, 0, 0, err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		var update Update
		if err = json.Unmarshal(raw, &update); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		updates = append(updates, update)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, 0, err
	}
	return updates, latest, oldest, nil
}

func (w *World) Run(ctx context.Context) {
	ticker := time.NewTicker(w.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Step(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "advance world tick: %v\n", err)
			}
		}
	}
}

func (w *World) Step(ctx context.Context) error {
	w.writer.Lock()
	defer w.writer.Unlock()
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var tick int64
	if err := tx.QueryRow(ctx, `SELECT tick FROM world_meta WHERE singleton = TRUE FOR UPDATE`).Scan(&tick); err != nil {
		return err
	}
	playerIDs, err := loadPlayerIDs(ctx, tx)
	if err != nil {
		return err
	}
	due, err := seasonEndIfDue(ctx, tx, w.now())
	if err != nil {
		return err
	}
	if due {
		if err := w.finishAndResetSeason(ctx, tx, playerIDs, w.now()); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	started, err := w.seasonHasStarted(ctx, tx, w.now())
	if err != nil {
		return err
	}
	if !started {
		return tx.Commit(ctx)
	}
	beforeUnits, err := loadUnits(ctx, tx, true)
	if err != nil {
		return err
	}
	beforeBuildings, err := loadBuildings(ctx, tx, true)
	if err != nil {
		return err
	}
	beforeDeposits, err := loadResourceDeposits(ctx, tx)
	if err != nil {
		return err
	}
	tick++
	if _, err := tx.Exec(ctx, `UPDATE world_meta SET tick = $1 WHERE singleton = TRUE`, tick); err != nil {
		return err
	}

	combatResult, err := w.advanceCombatTick(ctx, tx, tick, beforeUnits, beforeBuildings, beforeDeposits)
	if err != nil {
		return err
	}

	buildingRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, width, height, status, started_tick, build_ticks, completion_tick, health
		FROM buildings WHERE status = 'constructing'
		ORDER BY player_id, id FOR UPDATE`)
	if err != nil {
		return err
	}
	completedBuildings := map[string][]Building{}
	progressBuildings := map[string][]Building{}
	buildingsToUpdate := make([]Building, 0)
	for buildingRows.Next() {
		building, err := scanBuilding(buildingRows)
		if err != nil {
			buildingRows.Close()
			return err
		}
		buildingsToUpdate = append(buildingsToUpdate, building)
	}
	buildingRows.Close()
	if err := buildingRows.Err(); err != nil {
		return err
	}
	for _, building := range buildingsToUpdate {
		if building.CompletionTick != nil && *building.CompletionTick <= tick {
			building.Status = "complete"
			building.CompletionTick = nil
			setBuildingProgress(&building, tick)
			if _, err := tx.Exec(ctx, `UPDATE buildings SET status = 'complete', completion_tick = NULL WHERE id = $1`, building.ID); err != nil {
				return err
			}
			completedBuildings[building.OwnerID] = append(completedBuildings[building.OwnerID], building)
			continue
		}
		previousMilestone := constructionMilestone(building, tick-1)
		setBuildingProgress(&building, tick)
		if building.ProgressPct > previousMilestone {
			progressBuildings[building.OwnerID] = append(progressBuildings[building.OwnerID], building)
		}
	}
	for playerID, buildings := range progressBuildings {
		for i := range buildings {
			buildings[i] = w.enrichBuilding(buildings[i])
		}
		update := Update{PlayerID: playerID, Tick: tick, Type: "buildings.progress", Buildings: buildings}
		if _, err := appendEvent(ctx, tx, update); err != nil {
			return err
		}
	}
	for playerID, buildings := range completedBuildings {
		for i := range buildings {
			buildings[i] = w.enrichBuilding(buildings[i])
		}
		update := Update{PlayerID: playerID, Tick: tick, Type: "buildings.completed", Buildings: buildings}
		if _, err := appendEvent(ctx, tx, update); err != nil {
			return err
		}
	}

	if err := w.regenerateDeposits(ctx, tx, tick); err != nil {
		return err
	}
	if err := w.ensureWorkerRecovery(ctx, tx, tick); err != nil {
		return err
	}
	if err := w.completeTrainingOrders(ctx, tx, tick); err != nil {
		return err
	}

	afterUnits, err := loadUnits(ctx, tx, false)
	if err != nil {
		return err
	}
	afterDeposits, err := loadResourceDeposits(ctx, tx)
	if err != nil {
		return err
	}
	if err := w.advanceGathering(ctx, tx, tick, afterUnits, afterDeposits); err != nil {
		return err
	}
	afterDeposits, err = loadResourceDeposits(ctx, tx)
	if err != nil {
		return err
	}
	afterBuildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return err
	}
	for i := range afterBuildings {
		setBuildingProgress(&afterBuildings[i], tick)
	}
	if err := w.updateHillControl(ctx, tx, tick, playerIDs, afterUnits); err != nil {
		return err
	}
	if err := w.emitWorldEvents(ctx, tx, tick, playerIDs, beforeUnits, beforeBuildings, afterUnits, afterBuildings, beforeDeposits, afterDeposits, combatResult.destroyed, combatResult.destroyedBuildings); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *World) ApplyCommand(ctx context.Context, playerID string, command Command) (CommandResult, error) {
	w.writer.Lock()
	defer w.writer.Unlock()
	if command.ID == "" {
		return CommandResult{Reason: "command id is required"}, nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)

	var tick int64
	if err := tx.QueryRow(ctx, `SELECT tick FROM world_meta WHERE singleton = TRUE FOR UPDATE`).Scan(&tick); err != nil {
		return CommandResult{}, err
	}
	var startsAt, endsAt time.Time
	if err := tx.QueryRow(ctx, `SELECT starts_at,ends_at FROM world_season WHERE singleton=TRUE`).Scan(&startsAt, &endsAt); err != nil {
		return CommandResult{}, err
	}
	now := w.now()
	if now.Before(startsAt) || !now.Before(endsAt) {
		result := CommandResult{ID: command.ID, Reason: "season is not active"}
		if err := tx.Commit(ctx); err != nil {
			return CommandResult{}, err
		}
		return result, nil
	}
	var joined bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM season_players WHERE season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE) AND player_id=$1)`, playerID).Scan(&joined); err != nil {
		return CommandResult{}, err
	}
	if !joined {
		result := CommandResult{ID: command.ID, Reason: "player has not joined the current season"}
		if err := tx.Commit(ctx); err != nil {
			return CommandResult{}, err
		}
		return result, nil
	}
	prior, err := readCommandResult(ctx, tx, playerID, command.ID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return CommandResult{}, err
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CommandResult{}, err
	}

	result := CommandResult{ID: command.ID}
	switch command.Type {
	case "move":
		result, err = w.applyMove(ctx, tx, playerID, tick, command)
	case "attack":
		result, err = w.applyAttack(ctx, tx, playerID, tick, command)
	case "build":
		result, err = w.applyBuild(ctx, tx, playerID, tick, command)
	case "gather":
		result, err = w.applyGather(ctx, tx, playerID, tick, command)
	case "train":
		result, err = w.applyTrain(ctx, tx, playerID, tick, command)
	default:
		result.Reason = "unsupported command type"
	}
	if err != nil {
		return CommandResult{}, err
	}
	if err := insertCommandResult(ctx, tx, playerID, result); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func (w *World) applyMove(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	if command.Target == nil || !w.insideMap(command.Target.X, command.Target.Y) {
		result.Reason = "target must be within map bounds (0..999)"
		return result, nil
	}
	if len(command.UnitIDs) == 0 {
		result.Reason = "at least one unit is required"
		return result, nil
	}
	seen := make(map[string]struct{}, len(command.UnitIDs))
	for _, id := range command.UnitIDs {
		if _, exists := seen[id]; exists {
			result.Reason = "unit ids must be unique"
			return result, nil
		}
		seen[id] = struct{}{}
	}

	rows, err := tx.Query(ctx, `SELECT id FROM units WHERE player_id = $1 AND id = ANY($2::text[]) FOR UPDATE`, playerID, command.UnitIDs)
	if err != nil {
		return result, err
	}
	found := make(map[string]struct{}, len(command.UnitIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		found[id] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(found) != len(command.UnitIDs) {
		result.Reason = "one or more units are unavailable to this player"
		return result, nil
	}

	changed := make([]Unit, 0, len(command.UnitIDs))
	for _, id := range command.UnitIDs {
		targetX, targetY := command.Target.X, command.Target.Y
		unit, err := scanUnit(tx.QueryRow(ctx, `
			UPDATE units SET target_x = $3, target_y = $4, target_unit_id = NULL, target_building_id = NULL, attack_target_x = NULL, attack_target_y = NULL,
				gather_target_id = NULL, gather_progress = 0
			WHERE player_id = $1 AND id = $2
			RETURNING id, player_id, kind, x, y, target_x, target_y, health, target_unit_id, target_building_id, attack_target_x, attack_target_y, gather_target_id, gather_progress`, playerID, id, targetX, targetY))
		if err != nil {
			return result, err
		}
		changed = append(changed, unit)
	}
	update := Update{PlayerID: playerID, Tick: tick, Type: "orders.updated", Units: changed}
	sequence, err := appendEvent(ctx, tx, update)
	if err != nil {
		return result, err
	}
	result.Accepted, result.Sequence = true, sequence
	return result, nil
}

func (w *World) applyAttack(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	if (command.TargetUnitID == "") == (command.TargetBuildingID == "") {
		result.Reason = "exactly one of target_unit_id or target_building_id is required"
		return result, nil
	}
	if len(command.UnitIDs) == 0 {
		result.Reason = "at least one unit is required"
		return result, nil
	}
	seen := make(map[string]struct{}, len(command.UnitIDs))
	for _, id := range command.UnitIDs {
		if _, exists := seen[id]; exists {
			result.Reason = "unit ids must be unique"
			return result, nil
		}
		seen[id] = struct{}{}
	}

	units, err := loadUnits(ctx, tx, true)
	if err != nil {
		return result, err
	}
	buildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return result, err
	}
	visible := w.visibleEntities(playerID, units, buildings)
	targetX, targetY := 0, 0
	if command.TargetUnitID != "" {
		var target *Unit
		for i := range units {
			if units[i].ID == command.TargetUnitID && units[i].OwnerID != playerID {
				target = &units[i]
				break
			}
		}
		if target == nil || !visible[entityKey("unit:"+command.TargetUnitID)] {
			result.Reason = "target unit is unavailable or not visible"
			return result, nil
		}
		targetX, targetY = target.X, target.Y
	} else {
		var target *Building
		for i := range buildings {
			if buildings[i].ID == command.TargetBuildingID && buildings[i].OwnerID != playerID && w.buildingDefinition(buildings[i].Kind).Destructible {
				target = &buildings[i]
				break
			}
		}
		if target == nil || !visible[entityKey("building:"+command.TargetBuildingID)] {
			result.Reason = "target building is unavailable, indestructible, or not visible"
			return result, nil
		}
		targetX, targetY = target.X, target.Y
	}
	selected := make(map[string]Unit, len(command.UnitIDs))
	for _, unit := range units {
		if unit.OwnerID == playerID {
			selected[unit.ID] = unit
		}
	}
	if len(selected) < len(command.UnitIDs) {
		result.Reason = "one or more units are unavailable to this player"
		return result, nil
	}
	for _, id := range command.UnitIDs {
		if w.unitAttackDamage(selected[id].Kind) == 0 {
			result.Reason = "one or more selected units cannot attack"
			return result, nil
		}
	}

	changed := make([]Unit, 0, len(command.UnitIDs))
	for _, id := range command.UnitIDs {
		unit, err := scanUnit(tx.QueryRow(ctx, `
			UPDATE units SET target_x = NULL, target_y = NULL, target_unit_id = $3, target_building_id = $4, attack_target_x = $5, attack_target_y = $6,
				gather_target_id = NULL, gather_progress = 0
			WHERE player_id = $1 AND id = $2
			RETURNING id, player_id, kind, x, y, target_x, target_y, health, target_unit_id, target_building_id, attack_target_x, attack_target_y, gather_target_id, gather_progress`,
			playerID, id, nullableString(command.TargetUnitID), nullableString(command.TargetBuildingID), targetX, targetY))
		if err != nil {
			return result, err
		}
		changed = append(changed, unit)
	}
	sequence, err := appendEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "orders.updated", Units: changed})
	if err != nil {
		return result, err
	}
	result.Accepted, result.Sequence = true, sequence
	return result, nil
}

func (w *World) applyBuild(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	definition, exists := w.definitions[command.BuildingKind]
	if !exists || definition.Starting {
		result.Reason = "unknown or non-buildable building type"
		return result, nil
	}
	if command.X == nil || command.Y == nil || !w.insideMap(*command.X, *command.Y) || *command.X+definition.Width > w.config.World.MapSize || *command.Y+definition.Height > w.config.World.MapSize {
		result.Reason = "building footprint must fit within map bounds (0..999)"
		return result, nil
	}
	var queueBusy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM buildings WHERE player_id = $1 AND status = 'constructing')`, playerID).Scan(&queueBusy); err != nil {
		return result, err
	}
	if queueBusy {
		result.Reason = "construction queue is busy"
		return result, nil
	}
	var occupied bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM buildings
			WHERE x < $1 AND x + width > $2 AND y < $3 AND y + height > $4
		)`, *command.X+definition.Width, *command.X, *command.Y+definition.Height, *command.Y).Scan(&occupied); err != nil {
		return result, err
	}
	if occupied {
		result.Reason = "building footprint is occupied"
		return result, nil
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM units
			WHERE x >= $1 AND x < $2 AND y >= $3 AND y < $4
		)`, *command.X, *command.X+definition.Width, *command.Y, *command.Y+definition.Height).Scan(&occupied); err != nil {
		return result, err
	}
	if occupied {
		result.Reason = "building footprint contains a unit"
		return result, nil
	}
	var materials int64
	if err := tx.QueryRow(ctx, `SELECT materials FROM player_resources WHERE player_id = $1 FOR UPDATE`, playerID).Scan(&materials); err != nil {
		return result, err
	}
	if materials < definition.Cost {
		result.Reason = "not enough materials"
		return result, nil
	}
	materials -= definition.Cost
	if _, err := tx.Exec(ctx, `UPDATE player_resources SET materials = $2 WHERE player_id = $1`, playerID, materials); err != nil {
		return result, err
	}
	buildingID, err := newID()
	if err != nil {
		return result, err
	}
	completionTick := tick + definition.BuildTicks
	building, err := scanBuilding(tx.QueryRow(ctx, `
		INSERT INTO buildings (id, player_id, kind, x, y, width, height, status, started_tick, build_ticks, completion_tick, health)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'constructing',$8,$9,$10,$11)
		RETURNING id, player_id, kind, x, y, width, height, status, started_tick, build_ticks, completion_tick, health`,
		buildingID, playerID, definition.Kind, *command.X, *command.Y, definition.Width, definition.Height, tick, definition.BuildTicks, completionTick, definition.MaxHealth))
	if err != nil {
		return result, err
	}
	setBuildingProgress(&building, tick)
	building = w.enrichBuilding(building)
	update := Update{PlayerID: playerID, Tick: tick, Type: "buildings.started", Buildings: []Building{building}, Resources: []Resource{{Kind: "materials", Amount: materials}}}
	sequence, err := appendEvent(ctx, tx, update)
	if err != nil {
		return result, err
	}
	allUnits, err := loadUnits(ctx, tx, false)
	if err != nil {
		return result, err
	}
	allBuildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return result, err
	}
	players, err := loadPlayerIDs(ctx, tx)
	if err != nil {
		return result, err
	}
	for _, observerID := range players {
		if observerID == playerID {
			continue
		}
		visible := w.visibleEntities(observerID, allUnits, allBuildings)
		if visible[entityKey("building:"+building.ID)] {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: observerID, Tick: tick, Type: "buildings.spotted", Buildings: []Building{building}}); err != nil {
				return result, err
			}
		}
	}
	result.Accepted, result.Sequence = true, sequence
	return result, nil
}

func (w *World) applyGather(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	if command.DepositID == "" || len(command.UnitIDs) == 0 {
		result.Reason = "deposit_id and at least one worker unit_id are required"
		return result, nil
	}
	units, err := loadUnits(ctx, tx, true)
	if err != nil {
		return result, err
	}
	buildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return result, err
	}
	deposits, err := loadResourceDeposits(ctx, tx)
	if err != nil {
		return result, err
	}
	var deposit *ResourceDeposit
	for i := range deposits {
		if deposits[i].ID == command.DepositID && w.depositVisible(playerID, deposits[i], units, buildings) {
			deposit = &deposits[i]
			break
		}
	}
	if deposit == nil {
		result.Reason = "resource deposit is unavailable or not visible"
		return result, nil
	}
	seen := make(map[string]bool, len(command.UnitIDs))
	changed := make([]Unit, 0, len(command.UnitIDs))
	for _, unitID := range command.UnitIDs {
		if seen[unitID] {
			result.Reason = "unit ids must be unique"
			return result, nil
		}
		seen[unitID] = true
		var unit Unit
		found := false
		for _, candidate := range units {
			if candidate.ID == unitID && candidate.OwnerID == playerID {
				unit, found = candidate, true
				break
			}
		}
		if !found {
			result.Reason = "one or more units are unavailable to this player"
			return result, nil
		}
		if unit.Kind != "worker" {
			result.Reason = "only workers can gather materials"
			return result, nil
		}
	}
	for _, unitID := range command.UnitIDs {
		unit, err := scanUnit(tx.QueryRow(ctx, `
			UPDATE units SET gather_target_id=$3, gather_progress=0, target_x=NULL, target_y=NULL,
				target_unit_id=NULL, target_building_id=NULL, attack_target_x=NULL, attack_target_y=NULL
			WHERE player_id=$1 AND id=$2
			RETURNING id,player_id,kind,x,y,target_x,target_y,health,target_unit_id,target_building_id,attack_target_x,attack_target_y,gather_target_id,gather_progress`,
			playerID, unitID, command.DepositID))
		if err != nil {
			return result, err
		}
		changed = append(changed, unit)
	}
	sequence, err := appendEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "orders.updated", Units: changed})
	if err != nil {
		return result, err
	}
	result.Accepted, result.Sequence = true, sequence
	return result, nil
}

func (w *World) applyTrain(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	if command.BuildingID == "" {
		result.Reason = "building_id is required"
		return result, nil
	}
	unitDefinition, validKind := w.unitDefinitions[command.UnitKind]
	if !validKind || unitDefinition.TrainingTicks == 0 {
		result.Reason = "unit_kind is not trainable"
		return result, nil
	}
	var buildingStatus, buildingKind string
	err := tx.QueryRow(ctx, `SELECT kind,status FROM buildings WHERE id=$1 AND player_id=$2 FOR UPDATE`, command.BuildingID, playerID).Scan(&buildingKind, &buildingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		result.Reason = "training building is unavailable to this player"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if buildingKind != unitDefinition.TrainingBuilding || buildingStatus != "complete" {
		result.Reason = "training requires a completed " + unitDefinition.TrainingBuilding
		return result, nil
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_orders WHERE building_id=$1)`, command.BuildingID).Scan(&busy); err != nil {
		return result, err
	}
	if busy {
		result.Reason = "training queue is busy"
		return result, nil
	}
	var materials int64
	if err := tx.QueryRow(ctx, `SELECT materials FROM player_resources WHERE player_id=$1 FOR UPDATE`, playerID).Scan(&materials); err != nil {
		return result, err
	}
	if materials < unitDefinition.TrainingCost {
		result.Reason = "not enough materials"
		return result, nil
	}
	orderID, err := newID()
	if err != nil {
		return result, err
	}
	order := TrainingOrder{ID: orderID, BuildingID: command.BuildingID, UnitKind: command.UnitKind, StartedTick: tick, CompletionTick: tick + unitDefinition.TrainingTicks}
	if _, err := tx.Exec(ctx, `INSERT INTO training_orders (id,player_id,building_id,unit_kind,started_tick,completion_tick) VALUES ($1,$2,$3,$4,$5,$6)`, order.ID, playerID, order.BuildingID, order.UnitKind, order.StartedTick, order.CompletionTick); err != nil {
		return result, err
	}
	materials -= unitDefinition.TrainingCost
	if _, err := tx.Exec(ctx, `UPDATE player_resources SET materials=$2 WHERE player_id=$1`, playerID, materials); err != nil {
		return result, err
	}
	sequence, err := appendEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "training.started", Training: []TrainingOrder{order}, Resources: []Resource{{Kind: "materials", Amount: materials}}})
	if err != nil {
		return result, err
	}
	result.Accepted, result.Sequence = true, sequence
	return result, nil
}

func stepToward(x, y, targetX, targetY int) (int, int) {
	if x < targetX {
		x++
	} else if x > targetX {
		x--
	} else if y < targetY {
		y++
	} else if y > targetY {
		y--
	}
	return x, y
}

func (w *World) insideMap(x, y int) bool {
	return w.config.World.contains(x, y)
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func scanUnit(row pgx.Row) (Unit, error) {
	var unit Unit
	var targetX, targetY pgtype.Int4
	var targetUnitID pgtype.Text
	var attackTargetX, attackTargetY pgtype.Int4
	var gatherTargetID pgtype.Text
	var targetBuildingID pgtype.Text
	err := row.Scan(&unit.ID, &unit.OwnerID, &unit.Kind, &unit.X, &unit.Y, &targetX, &targetY, &unit.Health, &targetUnitID, &targetBuildingID, &attackTargetX, &attackTargetY, &gatherTargetID, &unit.GatherProgress)
	if targetX.Valid {
		value := int(targetX.Int32)
		unit.TargetX = &value
	}
	if targetY.Valid {
		value := int(targetY.Int32)
		unit.TargetY = &value
	}
	if targetUnitID.Valid {
		unit.AttackTargetID = &targetUnitID.String
	}
	if targetBuildingID.Valid {
		unit.AttackTargetBuildingID = &targetBuildingID.String
	}
	if attackTargetX.Valid {
		value := int(attackTargetX.Int32)
		unit.AttackTargetX = &value
	}
	if attackTargetY.Valid {
		value := int(attackTargetY.Int32)
		unit.AttackTargetY = &value
	}
	if gatherTargetID.Valid {
		unit.GatherTargetID = &gatherTargetID.String
	}
	return unit, err
}

func loadUnits(ctx context.Context, tx pgx.Tx, lock bool) ([]Unit, error) {
	query := `SELECT id, player_id, kind, x, y, target_x, target_y, health, target_unit_id, target_building_id, attack_target_x, attack_target_y, gather_target_id, gather_progress FROM units ORDER BY player_id, id`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	units := make([]Unit, 0)
	for rows.Next() {
		unit, err := scanUnit(rows)
		if err != nil {
			return nil, err
		}
		units = append(units, unit)
	}
	return units, rows.Err()
}

func scanBuilding(row pgx.Row) (Building, error) {
	var building Building
	var completionTick pgtype.Int8
	err := row.Scan(&building.ID, &building.OwnerID, &building.Kind, &building.X, &building.Y, &building.Width, &building.Height, &building.Status, &building.StartedTick, &building.BuildTicks, &completionTick, &building.Health)
	if completionTick.Valid {
		value := completionTick.Int64
		building.CompletionTick = &value
	}
	return building, err
}

func loadBuildings(ctx context.Context, tx pgx.Tx, lock bool) ([]Building, error) {
	query := `SELECT id, player_id, kind, x, y, width, height, status, started_tick, build_ticks, completion_tick, health FROM buildings ORDER BY player_id, id`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buildings := make([]Building, 0)
	for rows.Next() {
		building, err := scanBuilding(rows)
		if err != nil {
			return nil, err
		}
		buildings = append(buildings, building)
	}
	return buildings, rows.Err()
}

func setBuildingProgress(building *Building, tick int64) {
	if building.Status == "complete" {
		building.ProgressTicks = building.BuildTicks
		building.ProgressPct = 100
		return
	}
	progress := tick - building.StartedTick
	if progress < 0 {
		progress = 0
	}
	if progress > building.BuildTicks {
		progress = building.BuildTicks
	}
	building.ProgressTicks = progress
	if building.BuildTicks > 0 {
		building.ProgressPct = int(progress * 100 / building.BuildTicks)
	}
}

func constructionMilestone(building Building, tick int64) int {
	setBuildingProgress(&building, tick)
	for _, milestone := range [...]int{75, 50, 25} {
		if building.ProgressPct >= milestone {
			return milestone
		}
	}
	return 0
}

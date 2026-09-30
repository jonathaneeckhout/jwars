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

const (
	EventLimit = 2000
	MapSize    = 1000
)

type Unit struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
	Kind    string `json:"kind"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	TargetX *int   `json:"target_x,omitempty"`
	TargetY *int   `json:"target_y,omitempty"`
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
	CompletionTick *int64 `json:"completion_tick,omitempty"`
}

type Resource struct {
	Kind   string `json:"kind"`
	Amount int64  `json:"amount"`
}

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Command struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	UnitIDs      []string `json:"unit_ids,omitempty"`
	Target       *Point   `json:"target,omitempty"`
	BuildingKind string   `json:"building_kind,omitempty"`
	X            *int     `json:"x,omitempty"`
	Y            *int     `json:"y,omitempty"`
}

type CommandResult struct {
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type Update struct {
	PlayerID  string     `json:"player_id"`
	Sequence  int64      `json:"sequence"`
	Tick      int64      `json:"tick"`
	Type      string     `json:"type"`
	Units     []Unit     `json:"units,omitempty"`
	Buildings []Building `json:"buildings,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
}

type Snapshot struct {
	PlayerID  string     `json:"player_id"`
	Tick      int64      `json:"tick"`
	Sequence  int64      `json:"sequence"`
	Units     []Unit     `json:"units"`
	Buildings []Building `json:"buildings"`
	Resources []Resource `json:"resources"`
}

type BuildingDefinition struct {
	Kind       string `json:"kind"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Cost       int64  `json:"cost"`
	BuildTicks int64  `json:"build_ticks"`
	Starting   bool   `json:"starting,omitempty"`
	StartX     int    `json:"start_x,omitempty"`
	StartY     int    `json:"start_y,omitempty"`
}

type definitionFile struct {
	Buildings []BuildingDefinition `json:"buildings"`
}

type World struct {
	pool        *pgxpool.Pool
	definitions map[string]BuildingDefinition
	writer      sync.Mutex
}

func New(pool *pgxpool.Pool, definitionsPath string) (*World, error) {
	content, err := os.ReadFile(definitionsPath)
	if err != nil {
		return nil, fmt.Errorf("read building definitions: %w", err)
	}
	var file definitionFile
	if err := json.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("decode building definitions: %w", err)
	}
	definitions := make(map[string]BuildingDefinition, len(file.Buildings))
	for _, definition := range file.Buildings {
		if definition.Kind == "" || definition.Width < 1 || definition.Height < 1 || definition.Width > MapSize || definition.Height > MapSize {
			return nil, fmt.Errorf("invalid footprint or kind in building definition %q", definition.Kind)
		}
		if definition.Cost < 0 || (!definition.Starting && definition.BuildTicks <= 0) || (definition.Starting && (definition.StartX < 0 || definition.StartY < 0 || definition.StartX+definition.Width > MapSize || definition.StartY+definition.Height > MapSize)) {
			return nil, fmt.Errorf("invalid cost or build_ticks in building definition %q", definition.Kind)
		}
		if _, exists := definitions[definition.Kind]; exists {
			return nil, fmt.Errorf("duplicate building definition %q", definition.Kind)
		}
		definitions[definition.Kind] = definition
	}
	if len(definitions) == 0 {
		return nil, errors.New("building definitions must not be empty")
	}
	return &World{pool: pool, definitions: definitions}, nil
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
	if err := tx.QueryRow(ctx, `SELECT sequence FROM player_sequences WHERE player_id = $1`, playerID).Scan(&snapshot.Sequence); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, err
		}
	}

	unitRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, target_x, target_y
		FROM units WHERE player_id = $1 ORDER BY id`, playerID)
	if err != nil {
		return Snapshot{}, err
	}
	for unitRows.Next() {
		unit, err := scanUnit(unitRows)
		if err != nil {
			unitRows.Close()
			return Snapshot{}, err
		}
		snapshot.Units = append(snapshot.Units, unit)
	}
	unitRows.Close()
	if err := unitRows.Err(); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Units == nil {
		snapshot.Units = []Unit{}
	}

	buildingRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, width, height, status, completion_tick
		FROM buildings WHERE player_id = $1 ORDER BY id`, playerID)
	if err != nil {
		return Snapshot{}, err
	}
	for buildingRows.Next() {
		building, err := scanBuilding(buildingRows)
		if err != nil {
			buildingRows.Close()
			return Snapshot{}, err
		}
		snapshot.Buildings = append(snapshot.Buildings, building)
	}
	buildingRows.Close()
	if err := buildingRows.Err(); err != nil {
		return Snapshot{}, err
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
	ticker := time.NewTicker(time.Second)
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
	tick++
	if _, err := tx.Exec(ctx, `UPDATE world_meta SET tick = $1 WHERE singleton = TRUE`, tick); err != nil {
		return err
	}

	unitRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, target_x, target_y
		FROM units WHERE target_x IS NOT NULL AND target_y IS NOT NULL ORDER BY player_id, id FOR UPDATE`)
	if err != nil {
		return err
	}
	unitsToMove := make([]Unit, 0)
	for unitRows.Next() {
		unit, err := scanUnit(unitRows)
		if err != nil {
			unitRows.Close()
			return err
		}
		unitsToMove = append(unitsToMove, unit)
	}
	unitRows.Close()
	if err := unitRows.Err(); err != nil {
		return err
	}
	changedUnits := map[string][]Unit{}
	for _, unit := range unitsToMove {
		unit.X, unit.Y = stepToward(unit.X, unit.Y, *unit.TargetX, *unit.TargetY)
		if unit.X == *unit.TargetX && unit.Y == *unit.TargetY {
			unit.TargetX, unit.TargetY = nil, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE units SET x = $2, y = $3, target_x = $4, target_y = $5 WHERE id = $1`, unit.ID, unit.X, unit.Y, unit.TargetX, unit.TargetY); err != nil {
			unitRows.Close()
			return err
		}
		changedUnits[unit.OwnerID] = append(changedUnits[unit.OwnerID], unit)
	}
	for playerID, units := range changedUnits {
		update := Update{PlayerID: playerID, Tick: tick, Type: "units.changed", Units: units}
		if _, err := appendEvent(ctx, tx, update); err != nil {
			return err
		}
	}

	buildingRows, err := tx.Query(ctx, `
		SELECT id, player_id, kind, x, y, width, height, status, completion_tick
		FROM buildings WHERE status = 'constructing' AND completion_tick <= $1
		ORDER BY player_id, id FOR UPDATE`, tick)
	if err != nil {
		return err
	}
	completedBuildings := map[string][]Building{}
	buildingsToComplete := make([]Building, 0)
	for buildingRows.Next() {
		building, err := scanBuilding(buildingRows)
		if err != nil {
			buildingRows.Close()
			return err
		}
		buildingsToComplete = append(buildingsToComplete, building)
	}
	buildingRows.Close()
	if err := buildingRows.Err(); err != nil {
		return err
	}
	for _, building := range buildingsToComplete {
		building.Status = "complete"
		building.CompletionTick = nil
		if _, err := tx.Exec(ctx, `UPDATE buildings SET status = 'complete', completion_tick = NULL WHERE id = $1`, building.ID); err != nil {
			return err
		}
		completedBuildings[building.OwnerID] = append(completedBuildings[building.OwnerID], building)
	}
	for playerID, buildings := range completedBuildings {
		update := Update{PlayerID: playerID, Tick: tick, Type: "buildings.completed", Buildings: buildings}
		if _, err := appendEvent(ctx, tx, update); err != nil {
			return err
		}
	}

	if tick%60 == 0 {
		resourceRows, err := tx.Query(ctx, `SELECT player_id, materials FROM player_resources ORDER BY player_id FOR UPDATE`)
		if err != nil {
			return err
		}
		resourcesByPlayer := map[string]Resource{}
		type playerResource struct {
			playerID string
			amount   int64
		}
		balances := make([]playerResource, 0)
		for resourceRows.Next() {
			var balance playerResource
			if err := resourceRows.Scan(&balance.playerID, &balance.amount); err != nil {
				resourceRows.Close()
				return err
			}
			balances = append(balances, balance)
		}
		resourceRows.Close()
		if err := resourceRows.Err(); err != nil {
			return err
		}
		for _, balance := range balances {
			balance.amount++
			if _, err := tx.Exec(ctx, `UPDATE player_resources SET materials = $2 WHERE player_id = $1`, balance.playerID, balance.amount); err != nil {
				return err
			}
			resourcesByPlayer[balance.playerID] = Resource{Kind: "materials", Amount: balance.amount}
		}
		for playerID, resource := range resourcesByPlayer {
			update := Update{PlayerID: playerID, Tick: tick, Type: "resources.changed", Resources: []Resource{resource}}
			if _, err := appendEvent(ctx, tx, update); err != nil {
				return err
			}
		}
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
	case "build":
		result, err = w.applyBuild(ctx, tx, playerID, tick, command)
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
	if command.Target == nil || !insideMap(command.Target.X, command.Target.Y) {
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
			UPDATE units SET target_x = $3, target_y = $4
			WHERE player_id = $1 AND id = $2
			RETURNING id, player_id, kind, x, y, target_x, target_y`, playerID, id, targetX, targetY))
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

func (w *World) applyBuild(ctx context.Context, tx pgx.Tx, playerID string, tick int64, command Command) (CommandResult, error) {
	result := CommandResult{ID: command.ID}
	definition, exists := w.definitions[command.BuildingKind]
	if !exists || definition.Starting {
		result.Reason = "unknown or non-buildable building type"
		return result, nil
	}
	if command.X == nil || command.Y == nil || *command.X < 0 || *command.Y < 0 || *command.X+definition.Width > MapSize || *command.Y+definition.Height > MapSize {
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
	buildingID, err := newID("building")
	if err != nil {
		return result, err
	}
	completionTick := tick + definition.BuildTicks
	building, err := scanBuilding(tx.QueryRow(ctx, `
		INSERT INTO buildings (id, player_id, kind, x, y, width, height, status, completion_tick)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'constructing',$8)
		RETURNING id, player_id, kind, x, y, width, height, status, completion_tick`,
		buildingID, playerID, definition.Kind, *command.X, *command.Y, definition.Width, definition.Height, completionTick))
	if err != nil {
		return result, err
	}
	update := Update{PlayerID: playerID, Tick: tick, Type: "buildings.started", Buildings: []Building{building}, Resources: []Resource{{Kind: "materials", Amount: materials}}}
	sequence, err := appendEvent(ctx, tx, update)
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

func insideMap(x, y int) bool {
	return x >= 0 && x < MapSize && y >= 0 && y < MapSize
}

func newID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func scanUnit(row pgx.Row) (Unit, error) {
	var unit Unit
	var targetX, targetY pgtype.Int4
	err := row.Scan(&unit.ID, &unit.OwnerID, &unit.Kind, &unit.X, &unit.Y, &targetX, &targetY)
	if targetX.Valid {
		value := int(targetX.Int32)
		unit.TargetX = &value
	}
	if targetY.Valid {
		value := int(targetY.Int32)
		unit.TargetY = &value
	}
	return unit, err
}

func scanBuilding(row pgx.Row) (Building, error) {
	var building Building
	var completionTick pgtype.Int8
	err := row.Scan(&building.ID, &building.OwnerID, &building.Kind, &building.X, &building.Y, &building.Width, &building.Height, &building.Status, &completionTick)
	if completionTick.Valid {
		value := completionTick.Int64
		building.CompletionTick = &value
	}
	return building, err
}

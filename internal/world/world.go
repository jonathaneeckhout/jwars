package world

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const eventLimit = 2000

type Unit struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
	Kind    string `json:"kind"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	TargetX *int   `json:"target_x,omitempty"`
	TargetY *int   `json:"target_y,omitempty"`
}

type Update struct {
	PlayerID string `json:"player_id"`
	Sequence uint64 `json:"sequence"`
	Tick     uint64 `json:"tick"`
	Type     string `json:"type"`
	Units    []Unit `json:"units"`
}

type Snapshot struct {
	PlayerID string `json:"player_id"`
	Tick     uint64 `json:"tick"`
	Sequence uint64 `json:"sequence"`
	Units    []Unit `json:"units"`
}

type MoveCommand struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	UnitIDs []string `json:"unit_ids"`
	Target  struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"target"`
}

type CommandResult struct {
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
}

type diskState struct {
	Tick            uint64                   `json:"tick"`
	PlayerSequences map[string]uint64        `json:"player_sequences"`
	Units           map[string]Unit          `json:"units"`
	Events          map[string][]Update      `json:"events"`
	Commands        map[string]CommandResult `json:"commands"`
}

type World struct {
	mu   sync.Mutex
	path string
	data diskState
}

func Open(path string) (*World, error) {
	w := &World{path: path}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		w.data = newDiskState()
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &w.data); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if w.data.Units == nil {
		w.data.Units = map[string]Unit{}
	}
	if w.data.PlayerSequences == nil {
		w.data.PlayerSequences = map[string]uint64{}
	}
	if w.data.Events == nil {
		w.data.Events = map[string][]Update{}
	}
	if w.data.Commands == nil {
		w.data.Commands = map[string]CommandResult{}
	}
	return w, nil
}

func newDiskState() diskState {
	return diskState{
		Units: map[string]Unit{}, PlayerSequences: map[string]uint64{},
		Events: map[string][]Update{}, Commands: map[string]CommandResult{},
	}
}

func (w *World) EnsureDemoWorld(playerID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.data.Units) != 0 {
		return nil
	}
	w.data.Units["worker-1"] = Unit{ID: "worker-1", OwnerID: playerID, Kind: "worker", X: 2, Y: 2}
	w.data.Units["soldier-1"] = Unit{ID: "soldier-1", OwnerID: playerID, Kind: "soldier", X: 3, Y: 2}
	return w.persistLocked()
}

func (w *World) Snapshot(playerID string) Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshotLocked(playerID)
}

func (w *World) snapshotLocked(playerID string) Snapshot {
	units := make([]Unit, 0)
	for _, unit := range w.data.Units {
		if unit.OwnerID == playerID {
			units = append(units, unit)
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	return Snapshot{PlayerID: playerID, Tick: w.data.Tick, Sequence: w.data.PlayerSequences[playerID], Units: units}
}

func (w *World) UpdatesAfter(playerID string, sequence uint64) (updates []Update, latest uint64, oldest uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	latest = w.data.PlayerSequences[playerID]
	events := w.data.Events[playerID]
	if len(events) > 0 {
		oldest = events[0].Sequence
	}
	for _, update := range events {
		if update.Sequence > sequence {
			updates = append(updates, update)
		}
	}
	return
}

func (w *World) ApplyMove(playerID string, command MoveCommand) CommandResult {
	w.mu.Lock()
	defer w.mu.Unlock()
	commandKey := playerID + "/" + command.ID
	if prior, ok := w.data.Commands[commandKey]; ok {
		return prior
	}
	before := cloneState(w.data)
	result := CommandResult{ID: command.ID}
	if command.ID == "" {
		result.Reason = "command id is required"
		return result
	}
	if command.Type != "move" {
		result.Reason = "unsupported command type"
		return w.rememberLocked(commandKey, result)
	}
	if len(command.UnitIDs) == 0 {
		result.Reason = "at least one unit is required"
		return w.rememberLocked(commandKey, result)
	}
	if command.Target.X < 0 || command.Target.X > 999 || command.Target.Y < 0 || command.Target.Y > 999 {
		result.Reason = "target must be within the map bounds (0..999)"
		return w.rememberLocked(commandKey, result)
	}
	seen := make(map[string]bool, len(command.UnitIDs))
	units := make([]Unit, 0, len(command.UnitIDs))
	for _, id := range command.UnitIDs {
		if seen[id] {
			result.Reason = "unit ids must be unique"
			return w.rememberLocked(commandKey, result)
		}
		seen[id] = true
		unit, ok := w.data.Units[id]
		if !ok || unit.OwnerID != playerID {
			result.Reason = "one or more units are unavailable to this player"
			return w.rememberLocked(commandKey, result)
		}
		units = append(units, unit)
	}
	for i := range units {
		x, y := command.Target.X, command.Target.Y
		units[i].TargetX, units[i].TargetY = &x, &y
		w.data.Units[units[i].ID] = units[i]
	}
	update := w.appendUpdateLocked(playerID, "orders.updated", units)
	result.Accepted = true
	result.Sequence = update.Sequence
	w.data.Commands[commandKey] = result
	if err := w.persistLocked(); err != nil {
		w.data = before
		return CommandResult{ID: command.ID, Reason: "could not persist command"}
	}
	return result
}

func (w *World) rememberLocked(key string, result CommandResult) CommandResult {
	before := cloneState(w.data)
	w.data.Commands[key] = result
	if err := w.persistLocked(); err != nil {
		w.data = before
		return CommandResult{ID: result.ID, Reason: "could not persist command"}
	}
	return result
}

func (w *World) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.Step()
		}
	}
}

func (w *World) Step() {
	w.mu.Lock()
	defer w.mu.Unlock()
	before := cloneState(w.data)
	w.data.Tick++
	changedByOwner := map[string][]Unit{}
	for id, unit := range w.data.Units {
		if unit.TargetX == nil || unit.TargetY == nil {
			continue
		}
		if unit.X < *unit.TargetX {
			unit.X++
		} else if unit.X > *unit.TargetX {
			unit.X--
		} else if unit.Y < *unit.TargetY {
			unit.Y++
		} else if unit.Y > *unit.TargetY {
			unit.Y--
		}
		if unit.X == *unit.TargetX && unit.Y == *unit.TargetY {
			unit.TargetX, unit.TargetY = nil, nil
		}
		w.data.Units[id] = unit
		changedByOwner[unit.OwnerID] = append(changedByOwner[unit.OwnerID], unit)
	}
	for playerID, units := range changedByOwner {
		sort.Slice(units, func(i, j int) bool { return units[i].ID < units[j].ID })
		w.appendUpdateLocked(playerID, "units.changed", units)
	}
	if err := w.persistLocked(); err != nil {
		w.data = before
		fmt.Fprintf(os.Stderr, "persist world tick: %v\n", err)
	}
}

func cloneState(source diskState) diskState {
	clone := diskState{
		Tick:            source.Tick,
		PlayerSequences: make(map[string]uint64, len(source.PlayerSequences)),
		Units:           make(map[string]Unit, len(source.Units)),
		Events:          make(map[string][]Update, len(source.Events)),
		Commands:        make(map[string]CommandResult, len(source.Commands)),
	}
	for playerID, sequence := range source.PlayerSequences {
		clone.PlayerSequences[playerID] = sequence
	}
	for playerID, events := range source.Events {
		clone.Events[playerID] = append([]Update(nil), events...)
	}
	for id, unit := range source.Units {
		clone.Units[id] = unit
	}
	for id, result := range source.Commands {
		clone.Commands[id] = result
	}
	return clone
}

func (w *World) appendUpdateLocked(playerID, kind string, units []Unit) Update {
	w.data.PlayerSequences[playerID]++
	copyOfUnits := append([]Unit(nil), units...)
	update := Update{PlayerID: playerID, Sequence: w.data.PlayerSequences[playerID], Tick: w.data.Tick, Type: kind, Units: copyOfUnits}
	events := append(w.data.Events[playerID], update)
	if len(events) > eventLimit {
		events = append([]Update(nil), events[len(events)-eventLimit:]...)
	}
	w.data.Events[playerID] = events
	return update
}

func (w *World) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	content, err := json.Marshal(w.data)
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

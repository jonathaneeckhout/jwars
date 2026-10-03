package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jojo/jwars/internal/world"
)

func TestWorldSnapshot_RevealsVisibleEnemyUnits(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)

	snapshot := getSnapshot(t, player)
	var visibleEnemySoldier *world.Unit
	for _, unit := range snapshot.Units {
		if unit.OwnerID == enemy.playerID && unit.Kind == "soldier" {
			visibleEnemySoldier = &unit
		}
	}
	if visibleEnemySoldier == nil {
		t.Fatalf("snapshot does not include enemy soldier within starting vision: %+v", snapshot.Units)
	}
	if visibleEnemySoldier.Health != 100 || visibleEnemySoldier.MaxHealth != 100 || visibleEnemySoldier.VisionRange != 8 {
		t.Fatalf("enemy soldier stats = %+v, want 100/100 health and 8 vision", visibleEnemySoldier)
	}
}

func TestAttackCommand_DamagesVisibleEnemyAndEmitsEvent(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	attacker := ownedUnit(t, snapshot, player.playerID, "soldier")
	targetID := ownedUnitID(t, snapshot, enemy.playerID, "soldier")
	// Spawn sites are randomized and this test only asserts the damage step,
	// so put the target within the soldier's one-tile attack range first.
	positionUnit(t, targetID, attacker.X+1, attacker.Y)
	attackerID := attacker.ID
	result := postCommand(t, player, world.Command{
		ID: "attack-visible-enemy", Type: "attack", UnitIDs: []string{attackerID}, TargetUnitID: targetID,
	}, http.StatusAccepted)
	if !result.Accepted {
		t.Fatalf("attack command rejected: %+v", result)
	}
	stepWorld(t, 1)

	after := getSnapshot(t, player)
	target := unitByID(t, after, targetID)
	if target.Health != 80 {
		t.Fatalf("enemy health after one combat tick = %d, want 80", target.Health)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, _, _, err := testWorld.EventsAfter(ctx, player.playerID, result.Sequence)
	if err != nil {
		t.Fatalf("read combat events: %v", err)
	}
	if len(updates) != 1 || updates[0].Type != "units.damaged" || len(updates[0].Units) != 1 || updates[0].Units[0].Health != 80 {
		t.Fatalf("combat events = %+v, want one units.damaged event at 80 health", updates)
	}
}

func TestAttackCommand_RejectsWorkers(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	workerID := ownedUnitID(t, snapshot, player.playerID, "worker")
	targetID := ownedUnitID(t, snapshot, enemy.playerID, "soldier")
	result := postCommand(t, player, world.Command{
		ID: "worker-attack", Type: "attack", UnitIDs: []string{workerID}, TargetUnitID: targetID,
	}, http.StatusUnprocessableEntity)
	if result.Accepted || result.Reason != "one or more selected units cannot attack" {
		t.Fatalf("worker attack result = %+v, want cannot-attack rejection", result)
	}
}

func TestCombat_DestroyedUnitsAreRemovedAndEmitEvent(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	attacker := ownedUnit(t, snapshot, player.playerID, "soldier")
	targetID := ownedUnitID(t, snapshot, enemy.playerID, "soldier")
	positionUnit(t, targetID, attacker.X+1, attacker.Y)
	attackerID := attacker.ID
	result := postCommand(t, player, world.Command{
		ID: "attack-until-destroyed", Type: "attack", UnitIDs: []string{attackerID}, TargetUnitID: targetID,
	}, http.StatusAccepted)
	stepWorld(t, 5)

	for _, unit := range getSnapshot(t, player).Units {
		if unit.ID == targetID {
			t.Fatalf("destroyed enemy unit remains in snapshot: %+v", unit)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, _, _, err := testWorld.EventsAfter(ctx, player.playerID, result.Sequence)
	if err != nil {
		t.Fatalf("read destruction event: %v", err)
	}
	damagedEvents, destroyedEvent := 0, false
	for _, update := range updates {
		if update.Type == "units.damaged" {
			damagedEvents++
		}
		if update.Type == "units.destroyed" && len(update.Units) == 1 && update.Units[0].ID == targetID {
			destroyedEvent = true
		}
	}
	if damagedEvents != 4 || !destroyedEvent {
		t.Fatalf("combat events = %+v, want four damage events and a destruction event", updates)
	}
}

func TestWorldSnapshot_HidesEnemiesOutsideVisionAndEmitsHiddenEvent(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)
	initial := getSnapshot(t, player)
	enemySnapshot := getSnapshot(t, enemy)
	targetID := ownedUnitID(t, initial, enemy.playerID, "soldier")
	unitIDs := []string{
		ownedUnitID(t, enemySnapshot, enemy.playerID, "worker"),
		ownedUnitID(t, enemySnapshot, enemy.playerID, "soldier"),
	}
	postCommand(t, enemy, world.Command{
		ID: "leave-player-vision", Type: "move", UnitIDs: unitIDs, Target: &world.Point{X: 100, Y: 12},
	}, http.StatusAccepted)
	stepWorld(t, 20)
	for _, unit := range getSnapshot(t, player).Units {
		if unit.ID == targetID {
			t.Fatalf("enemy unit outside vision remains in snapshot: %+v", unit)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, _, _, err := testWorld.EventsAfter(ctx, player.playerID, initial.Sequence)
	if err != nil {
		t.Fatalf("read visibility events: %v", err)
	}
	foundHidden := false
	for _, update := range updates {
		if update.Type == "entities.hidden" {
			for _, entity := range update.Entities {
				if entity.ID == targetID && entity.Type == "unit" {
					foundHidden = true
				}
			}
		}
	}
	if !foundHidden {
		t.Fatalf("event history has no hidden event for enemy %s: %+v", targetID, updates)
	}
}

func ownedUnitID(t *testing.T, snapshot world.Snapshot, ownerID, kind string) string {
	return ownedUnit(t, snapshot, ownerID, kind).ID
}

func ownedUnit(t *testing.T, snapshot world.Snapshot, ownerID, kind string) world.Unit {
	t.Helper()
	for _, unit := range snapshot.Units {
		if unit.OwnerID == ownerID && unit.Kind == kind {
			return unit
		}
	}
	t.Fatalf("%s has no visible %s in snapshot", ownerID, kind)
	return world.Unit{}
}

func positionUnit(t *testing.T, unitID string, x, y int) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE units SET x=$2,y=$3 WHERE id=$1`, unitID, x, y); err != nil {
		t.Fatalf("position unit %s for combat fixture: %v", unitID, err)
	}
}

func unitByID(t *testing.T, snapshot world.Snapshot, id string) world.Unit {
	t.Helper()
	for _, unit := range snapshot.Units {
		if unit.ID == id {
			return unit
		}
	}
	t.Fatalf("unit %s is missing from snapshot", id)
	return world.Unit{}
}

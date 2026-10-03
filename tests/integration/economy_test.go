package integration_test

import (
	"net/http"
	"testing"

	"github.com/jojo/jwars/internal/world"
)

func TestGatherCommand_MovesWorkerAndCollectsMaterials(t *testing.T) {
	player := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	workerID := ownedUnitID(t, snapshot, player.playerID, "worker")
	var deposit world.ResourceDeposit
	for _, candidate := range snapshot.Deposits {
		if candidate.X == 20 && candidate.Y == 12 {
			deposit = candidate
			break
		}
	}
	if deposit.ID == "" {
		t.Fatalf("starting snapshot does not reveal nearby material deposit: %+v", snapshot.Deposits)
	}

	result := postCommand(t, player, world.Command{
		ID: "gather-nearby-materials", Type: "gather", DepositID: deposit.ID, UnitIDs: []string{workerID},
	}, http.StatusAccepted)
	if !result.Accepted {
		t.Fatalf("gather order rejected: %+v", result)
	}
	for tick := 0; tick < 20; tick++ {
		stepWorld(t, 1)
		current := getSnapshot(t, player)
		worker := unitByID(t, current, workerID)
		if maxDistance(worker.X, worker.Y, deposit.X, deposit.Y) <= 1 {
			break
		}
		if tick == 19 {
			t.Fatal("worker did not reach the resource deposit")
		}
	}
	stepWorld(t, 5)
	after := getSnapshot(t, player)
	if resourceAmount(t, after, "materials") != 101 {
		t.Fatalf("materials after one harvest = %d, want 101", resourceAmount(t, after, "materials"))
	}
	for _, current := range after.Deposits {
		if current.ID == deposit.ID {
			if current.Amount != deposit.Amount-1 {
				t.Fatalf("deposit amount after harvest = %d, want %d", current.Amount, deposit.Amount-1)
			}
			return
		}
	}
	t.Fatalf("harvested deposit missing from snapshot: %+v", after.Deposits)
}

func TestTrainCommand_CompletesMeleeAndRangedTraining(t *testing.T) {
	player := newTestPlayer(t)
	// Keep this fixture away from the persistent construction fixtures at
	// (30,30), (40,40), and (50,50).
	buildingX, buildingY := 60, 60
	built := postCommand(t, player, world.Command{
		ID: "build-barracks", Type: "build", BuildingKind: "barracks", X: &buildingX, Y: &buildingY,
	}, http.StatusAccepted)
	if !built.Accepted {
		t.Fatalf("barracks build rejected: %+v", built)
	}
	stepWorld(t, 4)
	snapshot := getSnapshot(t, player)
	barracks := buildingByKind(t, snapshot, "barracks")

	for _, unitKind := range []string{"soldier", "archer"} {
		before := countOwnedUnits(snapshot, player.playerID)
		beforeIDs := make(map[string]bool, len(snapshot.Units))
		for _, unit := range snapshot.Units {
			beforeIDs[unit.ID] = true
		}
		result := postCommand(t, player, world.Command{
			ID: "train-" + unitKind, Type: "train", BuildingID: barracks.ID, UnitKind: unitKind,
		}, http.StatusAccepted)
		if !result.Accepted {
			t.Fatalf("%s training rejected: %+v", unitKind, result)
		}
		duration := 15
		if unitKind == "archer" {
			duration = 25
		}
		stepWorld(t, duration)
		snapshot = getSnapshot(t, player)
		if got := countOwnedUnits(snapshot, player.playerID); got != before+1 {
			t.Fatalf("owned unit count after training %s = %d, want %d", unitKind, got, before+1)
		}
		var trained *world.Unit
		for _, unit := range snapshot.Units {
			if unit.OwnerID == player.playerID && !beforeIDs[unit.ID] {
				trained = &unit
				break
			}
		}
		if trained == nil || trained.Kind != unitKind || trained.MaxHealth != unitMaxHealthForTest(unitKind) {
			t.Fatalf("newly trained %s unit = %+v", unitKind, trained)
		}
	}
}

func TestWatchtower_AutomaticallyDamagesNearbyEnemy(t *testing.T) {
	player := newTestPlayer(t)
	enemy := newTestPlayer(t)
	// Keep the tower away from the hill and close to the enemy's starting unit.
	x, y := 40, 12
	result := postCommand(t, player, world.Command{
		ID: "build-watchtower", Type: "build", BuildingKind: "watchtower", X: &x, Y: &y,
	}, http.StatusAccepted)
	if !result.Accepted {
		t.Fatalf("watchtower build rejected: %+v", result)
	}
	enemySnapshot := getSnapshot(t, enemy)
	enemySoldier := ownedUnitID(t, enemySnapshot, enemy.playerID, "soldier")
	target := world.Point{X: 42, Y: 12}
	if result := postCommand(t, enemy, world.Command{
		ID: "move-soldier-near-tower", Type: "move", UnitIDs: []string{enemySoldier}, Target: &target,
	}, http.StatusAccepted); !result.Accepted {
		t.Fatalf("move near watchtower rejected: %+v", result)
	}
	stepWorld(t, 40)
	unit := unitByID(t, getSnapshot(t, player), enemySoldier)
	if unit.Health >= unit.MaxHealth {
		t.Fatalf("watchtower did not damage the nearby enemy: %+v", unit)
	}
}

func resourceAmount(t *testing.T, snapshot world.Snapshot, kind string) int64 {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource.Amount
		}
	}
	t.Fatalf("snapshot does not contain resource %q: %+v", kind, snapshot.Resources)
	return 0
}

func countOwnedUnits(snapshot world.Snapshot, playerID string) int {
	count := 0
	for _, unit := range snapshot.Units {
		if unit.OwnerID == playerID {
			count++
		}
	}
	return count
}

func maxDistance(x, y, targetX, targetY int) int {
	dx, dy := absCoordinate(x-targetX), absCoordinate(y-targetY)
	if dx > dy {
		return dx
	}
	return dy
}

func absCoordinate(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func unitMaxHealthForTest(kind string) int {
	if kind == "archer" {
		return 60
	}
	return 100
}

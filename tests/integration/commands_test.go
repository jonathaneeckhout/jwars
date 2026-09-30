package integration_test

import (
	"net/http"
	"testing"

	"github.com/jojo/jwars/internal/world"
)

func TestMoveCommand_AcceptsAndIsIdempotent(t *testing.T) {
	player := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	unit := snapshot.Units[0]
	command := world.Command{
		ID:      "move-once",
		Type:    "move",
		UnitIDs: []string{unit.ID},
		Target:  &world.Point{X: unit.X + 1, Y: unit.Y},
	}

	first := postCommand(t, player, command, http.StatusAccepted)
	if !first.Accepted || first.Sequence == 0 {
		t.Fatalf("move was not accepted with a sequence: %+v", first)
	}
	duplicate := postCommand(t, player, command, http.StatusAccepted)
	if duplicate.Sequence != first.Sequence {
		t.Fatalf("repeated command sequence = %d, want original %d", duplicate.Sequence, first.Sequence)
	}

	stepWorld(t, 1)
	afterMove := getSnapshot(t, player)
	for _, movedUnit := range afterMove.Units {
		if movedUnit.ID == unit.ID {
			if movedUnit.X != unit.X+1 || movedUnit.TargetX != nil {
				t.Fatalf("unit after move = %+v, want x=%d and no remaining target", movedUnit, unit.X+1)
			}
			return
		}
	}
	t.Fatalf("moved unit %q missing from snapshot", unit.ID)
}

func TestMoveCommand_RejectsUnavailableUnits(t *testing.T) {
	player := newTestPlayer(t)
	command := world.Command{
		ID:      "move-unavailable-unit",
		Type:    "move",
		UnitIDs: []string{"not-owned-by-player"},
		Target:  &world.Point{X: 20, Y: 20},
	}
	result := postCommand(t, player, command, http.StatusUnprocessableEntity)
	if result.Accepted || result.Reason == "" {
		t.Fatalf("invalid move should be rejected with a reason: %+v", result)
	}
}

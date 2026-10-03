package integration_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/jojo/jwars/internal/world"
)

func TestWorldSnapshot_RequiresAuthentication(t *testing.T) {
	apiDo(t, http.MethodGet, "/v1/world", "", nil, nil, http.StatusUnauthorized)
}

func TestUnitDefinitions_ExposeConfiguredStatsAndTrainingCosts(t *testing.T) {
	player := newTestPlayer(t)
	var response struct {
		Units []world.UnitDefinition `json:"units"`
	}
	apiDo(t, http.MethodGet, "/v1/definitions/units", player.token, nil, &response, http.StatusOK)
	if !reflect.DeepEqual(response.Units, testWorld.UnitDefinitions()) {
		t.Fatalf("API unit definitions = %+v, want loaded configuration %+v", response.Units, testWorld.UnitDefinitions())
	}
}

func TestWorldSnapshot_ListsRandomEntityIDs(t *testing.T) {
	player := newTestPlayer(t)
	snapshot := getSnapshot(t, player)

	ownedUnits, ownedBuildings := 0, 0
	seen := make(map[string]bool, len(snapshot.Units)+len(snapshot.Buildings))
	for _, unit := range snapshot.Units {
		if unit.OwnerID == player.playerID {
			ownedUnits++
		}
		assertRandomEntityID(t, unit.ID)
		if seen[unit.ID] {
			t.Fatalf("duplicate entity ID %q", unit.ID)
		}
		seen[unit.ID] = true
	}
	for _, building := range snapshot.Buildings {
		if building.OwnerID == player.playerID {
			ownedBuildings++
		}
		assertRandomEntityID(t, building.ID)
		if seen[building.ID] {
			t.Fatalf("duplicate entity ID %q", building.ID)
		}
		seen[building.ID] = true
	}
	if ownedUnits != 2 {
		t.Fatalf("snapshot has %d owned units, want 2", ownedUnits)
	}
	if ownedBuildings != 1 {
		t.Fatalf("snapshot has %d owned buildings, want 1", ownedBuildings)
	}
}

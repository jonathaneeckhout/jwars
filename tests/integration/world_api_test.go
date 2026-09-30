package integration_test

import (
	"net/http"
	"testing"
)

func TestWorldSnapshot_RequiresAuthentication(t *testing.T) {
	apiDo(t, http.MethodGet, "/v1/world", "", nil, nil, http.StatusUnauthorized)
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

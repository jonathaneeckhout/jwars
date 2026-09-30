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
	if len(snapshot.Units) != 2 {
		t.Fatalf("snapshot has %d units, want 2", len(snapshot.Units))
	}
	if len(snapshot.Buildings) != 1 {
		t.Fatalf("snapshot has %d buildings, want 1", len(snapshot.Buildings))
	}

	seen := make(map[string]bool, len(snapshot.Units)+len(snapshot.Buildings))
	for _, unit := range snapshot.Units {
		assertRandomEntityID(t, unit.ID)
		if seen[unit.ID] {
			t.Fatalf("duplicate entity ID %q", unit.ID)
		}
		seen[unit.ID] = true
	}
	assertRandomEntityID(t, snapshot.Buildings[0].ID)
	if seen[snapshot.Buildings[0].ID] {
		t.Fatalf("duplicate entity ID %q", snapshot.Buildings[0].ID)
	}
}

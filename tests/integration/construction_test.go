package integration_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jojo/jwars/internal/world"
)

func TestConstructionProgress_AppearsInSnapshots(t *testing.T) {
	player := newTestPlayer(t)
	result := buildAt(t, player, "snapshot-hut", 30, 30)
	if !result.Accepted {
		t.Fatalf("build command was rejected: %+v", result)
	}
	stepWorld(t, 2)

	building := buildingByKind(t, getSnapshot(t, player), "test_hut")
	assertRandomEntityID(t, building.ID)
	if building.Status != "constructing" || building.BuildTicks != 4 || building.ProgressTicks != 2 || building.ProgressPct != 50 {
		t.Fatalf("snapshot building progress = %+v, want constructing at 2/4 ticks (50%%)", building)
	}
}

func TestConstructionProgress_EmitsMilestonesAndCompletes(t *testing.T) {
	player := newTestPlayer(t)
	result := buildAt(t, player, "milestone-hut", 40, 40)
	stepWorld(t, 4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, _, _, err := testWorld.EventsAfter(ctx, player.playerID, result.Sequence)
	if err != nil {
		t.Fatalf("read construction event history: %v", err)
	}
	wantTypes := []string{"buildings.progress", "buildings.progress", "buildings.progress", "buildings.completed"}
	gotTypes := make([]string, 0, len(updates))
	for _, update := range updates {
		gotTypes = append(gotTypes, update.Type)
	}
	if fmt.Sprint(gotTypes) != fmt.Sprint(wantTypes) {
		t.Fatalf("construction event types = %v, want %v", gotTypes, wantTypes)
	}
	for i, wantPercent := range []int{25, 50, 75} {
		if len(updates[i].Buildings) != 1 || updates[i].Buildings[0].ProgressPct != wantPercent {
			t.Fatalf("progress event %d = %+v, want one building at %d%%", i, updates[i].Buildings, wantPercent)
		}
	}
	completed := updates[3].Buildings
	if len(completed) != 1 || completed[0].Status != "complete" || completed[0].ProgressPct != 100 {
		t.Fatalf("completion event = %+v, want one complete building at 100%%", completed)
	}

	streamedTypes := readSSEEvents(t, player, result.Sequence, len(wantTypes))
	if fmt.Sprint(streamedTypes) != fmt.Sprint(wantTypes) {
		t.Fatalf("SSE replay event types = %v, want %v", streamedTypes, wantTypes)
	}
	building := buildingByKind(t, getSnapshot(t, player), "test_hut")
	if building.Status != "complete" || building.ProgressPct != 100 {
		t.Fatalf("completed snapshot building = %+v, want complete at 100%%", building)
	}
}

func TestConstructionProgress_SurvivesWorldReinitialization(t *testing.T) {
	player := newTestPlayer(t)
	result := buildAt(t, player, "persistent-hut", 50, 50)
	if !result.Accepted {
		t.Fatalf("build command was rejected: %+v", result)
	}
	stepWorld(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before, err := testWorld.Snapshot(ctx, player.playerID)
	if err != nil {
		t.Fatalf("read snapshot before reinitialization: %v", err)
	}

	reloadedWorld, err := world.NewWithOptions(testPool, testVariablesDir, world.Options{
		Hill:       world.Point{X: 20, Y: 20},
		HillRadius: 1,
		Now:        func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatalf("create reloaded world: %v", err)
	}
	if err := reloadedWorld.Initialize(ctx, player.playerID, player.token); err != nil {
		t.Fatalf("reinitialize world: %v", err)
	}
	after, err := reloadedWorld.Snapshot(ctx, player.playerID)
	if err != nil {
		t.Fatalf("read reloaded snapshot: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot changed after world reinitialization:\nbefore: %+v\nafter:  %+v", before, after)
	}
}

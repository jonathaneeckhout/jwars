package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jojo/jwars/internal/world"
)

type testScoreboard struct {
	Season struct {
		Number int64 `json:"number"`
		EndsAt time.Time `json:"ends_at"`
	} `json:"season"`
	Hill struct {
		X           int     `json:"x"`
		Y           int     `json:"y"`
		Radius      int     `json:"control_radius"`
		OwnerID     *string `json:"owner_player_id"`
		Contested   bool    `json:"contested"`
	} `json:"hill"`
	Standings []struct {
		PlayerID       string `json:"player_id"`
		Score          int64  `json:"score"`
		ControlSeconds int64  `json:"control_seconds"`
	} `json:"standings"`
	LastCompleted *struct {
		Season struct { Number int64 `json:"number"` } `json:"season"`
		Winners []string `json:"winners"`
	} `json:"last_completed,omitempty"`
}

func getScoreboard(t *testing.T, player testIdentity) testScoreboard {
	t.Helper()
	var scoreboard testScoreboard
	apiDo(t, http.MethodGet, "/v1/scoreboard", player.token, nil, &scoreboard, http.StatusOK)
	return scoreboard
}

func TestHillControl_ScoresAndSeasonResetPreservesWinner(t *testing.T) {
	player := newTestPlayer(t)
	snapshot := getSnapshot(t, player)
	scoreboard := getScoreboard(t, player)
	if scoreboard.Hill.X != 20 || scoreboard.Hill.Y != 20 || scoreboard.Hill.Radius != 1 {
		t.Fatalf("test hill = %+v, want configured position (20,20) radius 1", scoreboard.Hill)
	}
	soldierID := ownedUnitID(t, snapshot, player.playerID, "soldier")
	result := postCommand(t, player, world.Command{
		ID: "move-soldier-to-hill", Type: "move", UnitIDs: []string{soldierID},
		Target: &world.Point{X: scoreboard.Hill.X, Y: scoreboard.Hill.Y},
	}, http.StatusAccepted)
	controlling := false
	for tick := 0; tick < 100; tick++ {
		stepWorld(t, 1)
		board := getScoreboard(t, player)
		if board.Hill.OwnerID != nil && *board.Hill.OwnerID == player.playerID {
			controlling = true
			break
		}
	}
	if !controlling {
		t.Fatal("soldier did not reach the hill within 100 ticks")
	}
	// The tick that first establishes control already counts toward the minute.
	stepWorld(t, 59)

	scoreboard = getScoreboard(t, player)
	standing := standingFor(t, scoreboard, player.playerID)
	if standing.Score != 1 || standing.ControlSeconds != 60 {
		t.Fatalf("standing after one minute of control = %+v, want score 1 after 60 seconds", standing)
	}
	if scoreboard.Hill.OwnerID == nil || *scoreboard.Hill.OwnerID != player.playerID || scoreboard.Hill.Contested {
		t.Fatalf("hill state after uncontested control = %+v, want controlled by %s", scoreboard.Hill, player.playerID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, _, _, err := testWorld.EventsAfter(ctx, player.playerID, result.Sequence)
	if err != nil {
		t.Fatalf("read score event: %v", err)
	}
	foundScoreEvent := false
	for _, update := range updates {
		if update.Type == "score.changed" {
			foundScoreEvent = true
		}
	}
	if !foundScoreEvent {
		t.Fatalf("score changes were not emitted after control: %+v", updates)
	}

	originalTime := testNow
	defer func() { testNow = originalTime }()
	testNow = scoreboard.Season.EndsAt
	stepWorld(t, 1)
	newSeason := getScoreboard(t, player)
	if newSeason.Season.Number != scoreboard.Season.Number+1 {
		t.Fatalf("season after expiry = %d, want %d", newSeason.Season.Number, scoreboard.Season.Number+1)
	}
	if standingFor(t, newSeason, player.playerID).Score != 0 {
		t.Fatalf("new season did not reset the player's score: %+v", standingFor(t, newSeason, player.playerID))
	}
	if newSeason.LastCompleted == nil || newSeason.LastCompleted.Season.Number != scoreboard.Season.Number || len(newSeason.LastCompleted.Winners) != 1 || newSeason.LastCompleted.Winners[0] != player.playerID {
		t.Fatalf("completed season result = %+v, want player %s as season %d winner", newSeason.LastCompleted, player.playerID, scoreboard.Season.Number)
	}
}

func standingFor(t *testing.T, scoreboard testScoreboard, playerID string) struct {
	PlayerID       string `json:"player_id"`
	Score          int64  `json:"score"`
	ControlSeconds int64  `json:"control_seconds"`
} {
	t.Helper()
	for _, standing := range scoreboard.Standings {
		if standing.PlayerID == playerID {
			return standing
		}
	}
	t.Fatalf("scoreboard has no standing for player %s: %+v", playerID, scoreboard.Standings)
	return struct {
		PlayerID       string `json:"player_id"`
		Score          int64  `json:"score"`
		ControlSeconds int64  `json:"control_seconds"`
	}{}
}

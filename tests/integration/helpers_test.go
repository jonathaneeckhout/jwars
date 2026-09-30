package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jojo/jwars/internal/world"
)

var randomEntityIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type testIdentity struct {
	playerID string
	token    string
}

func newTestPlayer(t *testing.T) testIdentity {
	t.Helper()
	identifier := fmt.Sprintf("%d", time.Now().UnixNano())
	player := testIdentity{
		playerID: "integration-player-" + identifier,
		token:    "integration-token-" + identifier,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := testWorld.Initialize(ctx, player.playerID, player.token); err != nil {
		t.Fatalf("initialize player %q: %v", player.playerID, err)
	}
	return player
}

func apiDo(t *testing.T, method, path, token string, body any, target any, wantStatus int) []byte {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, testServer.URL+path, requestBody)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: want HTTP %d, got %d: %s", method, path, wantStatus, response.StatusCode, responseBody)
	}
	if target != nil {
		if err := json.Unmarshal(responseBody, target); err != nil {
			t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return responseBody
}

func getSnapshot(t *testing.T, player testIdentity) world.Snapshot {
	t.Helper()
	var snapshot world.Snapshot
	apiDo(t, http.MethodGet, "/v1/world", player.token, nil, &snapshot, http.StatusOK)
	return snapshot
}

func postCommand(t *testing.T, player testIdentity, command world.Command, wantStatus int) world.CommandResult {
	t.Helper()
	var result world.CommandResult
	apiDo(t, http.MethodPost, "/v1/commands", player.token, command, &result, wantStatus)
	return result
}

func buildAt(t *testing.T, player testIdentity, id string, x, y int) world.CommandResult {
	t.Helper()
	return postCommand(t, player, world.Command{
		ID: id, Type: "build", BuildingKind: "test_hut", X: &x, Y: &y,
	}, http.StatusAccepted)
}

func stepWorld(t *testing.T, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range count {
		if err := testWorld.Step(ctx); err != nil {
			t.Fatalf("advance world tick: %v", err)
		}
	}
}

func readSSEEvents(t *testing.T, player testIdentity, after int64, count int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/events?after=%d", testServer.URL, after), nil)
	if err != nil {
		t.Fatalf("create events request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+player.token)
	response, err := testServer.Client().Do(request)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("event stream: want HTTP %d, got %d", http.StatusOK, response.StatusCode)
	}

	events := make([]string, 0, count)
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
			if len(events) == count {
				return events
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		t.Fatalf("read event stream: %v", err)
	}
	t.Fatalf("got %d SSE events, want %d: %v", len(events), count, events)
	return nil
}

func assertRandomEntityID(t *testing.T, id string) {
	t.Helper()
	if !randomEntityIDPattern.MatchString(id) {
		t.Fatalf("entity ID %q is not a prefix-free random 128-bit hex ID", id)
	}
}

func buildingByKind(t *testing.T, snapshot world.Snapshot, kind string) world.Building {
	t.Helper()
	for _, building := range snapshot.Buildings {
		if building.Kind == kind {
			return building
		}
	}
	t.Fatalf("snapshot does not contain building kind %q", kind)
	return world.Building{}
}

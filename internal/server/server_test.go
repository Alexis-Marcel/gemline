package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptest.NewServer(New(log, NewStore(nil), Config{}).Routes())
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestCreateGameStartsWaiting(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	if g.Status != StatusWaiting {
		t.Fatalf("want status waiting, got %s", g.Status)
	}
	if len(g.Seats) != 2 {
		t.Fatalf("want 2 seats, got %d", len(g.Seats))
	}
}

func TestJoinAndMove(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)

	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	if j1.Game.Status != StatusWaiting {
		t.Fatalf("game should remain waiting after first join")
	}
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	if j2.Game.Status != StatusPlaying {
		t.Fatalf("game should transition to playing once all seats are filled")
	}

	// Player 1 (Alice, seat 0, color C1) plays first.
	mr := postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	if mr.Game.Turn != 1 {
		t.Fatalf("want turn=1 after first move, got %d", mr.Game.Turn)
	}
	// Bob plays on Alice's cell → should fail (occupied).
	_ = postMove(t, ts, g.ID, j2.Token, 0, 0, http.StatusBadRequest)
}

func TestMoveRequiresToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)

	body := bytes.NewBufferString(`{"q":0,"r":0}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+g.ID+"/moves", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", resp.StatusCode)
	}
}

func TestMoveRejectsForeignToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	// Alice's token but it's Alice's turn first anyway; play once, then Alice
	// tries to play again with her token → ErrWrongTurn (server's color check).
	_ = postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	_ = postMove(t, ts, g.ID, j1.Token, 1, 0, http.StatusBadRequest)
}

func TestJoinRejectsFullGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	// third join on a 2-player game should fail
	body := bytes.NewBufferString(`{"name":"Charlie"}`)
	resp, err := http.Post(ts.URL+"/api/games/"+g.ID+"/join", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

func TestWebSocketBroadcastsMoves(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/games/" + g.ID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	// First message should be the initial state snapshot.
	first := readEvent(t, ctx, conn)
	if first.Type != "state" {
		t.Fatalf("want first event type=state, got %s", first.Type)
	}

	// Trigger a move; we should receive a "move" event over the WS.
	_ = postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)

	got := readEvent(t, ctx, conn)
	if got.Type != "move" {
		t.Fatalf("want move event, got %s", got.Type)
	}
}

// ---- helpers ----

func createGame(t *testing.T, ts *httptest.Server, players int) gameDTO {
	t.Helper()
	body := strings.NewReader(`{"players":` + itoa(players) + `}`)
	resp, err := http.Post(ts.URL+"/api/games", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d", resp.StatusCode)
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func joinGame(t *testing.T, ts *httptest.Server, id, name string, seat *int) joinResponse {
	t.Helper()
	payload := map[string]any{"name": name}
	if seat != nil {
		payload["seat"] = *seat
	}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/api/games/"+id+"/join", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("join: status=%d body=%s", resp.StatusCode, body)
	}
	var j joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	return j
}

func postMove(t *testing.T, ts *httptest.Server, id, token string, q, r, wantStatus int) moveResponse {
	t.Helper()
	body := strings.NewReader(`{"q":` + itoa(q) + `,"r":` + itoa(r) + `}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+id+"/moves", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Player-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("move: want status=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusOK {
		return moveResponse{}
	}
	var mr moveResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		t.Fatal(err)
	}
	return mr
}

func readEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) Event {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func itoa(i int) string {
	// avoid importing strconv just for tests
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

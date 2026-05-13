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

	"github.com/alexis/gemline/internal/game"
	"github.com/coder/websocket"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Bots play instantly in tests so assertions don't have to sleep.
	store := NewStore(nil).WithBotDelay(0)
	return httptest.NewServer(New(log, store, Config{}).Routes())
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
	if g.Visibility != VisibilityPrivate {
		t.Fatalf("want default visibility private, got %q", g.Visibility)
	}
}

func TestCreateGameWithPublicVisibility(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGameWithBody(t, ts, `{"players":2,"visibility":"public"}`, http.StatusCreated)
	if g.Visibility != VisibilityPublic {
		t.Fatalf("want public, got %q", g.Visibility)
	}
}

func TestCreateGameRejectsBadVisibility(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	_ = createGameWithBody(t, ts, `{"players":2,"visibility":"unicorn"}`, http.StatusBadRequest)
}

func TestRematchRequiresFinishedGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	resp, err := http.Post(ts.URL+"/api/games/"+g.ID+"/rematch", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 on rematch of waiting game, got %d", resp.StatusCode)
	}
}

func TestRematchIsIdempotent(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGameWithBody(t, ts, `{"players":2,"visibility":"public"}`, http.StatusCreated)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)

	r1 := postRematch(t, ts, g.ID, http.StatusCreated)
	r2 := postRematch(t, ts, g.ID, http.StatusCreated)
	if r1.GameID != r2.GameID {
		t.Fatalf("rematch must be idempotent; got %s then %s", r1.GameID, r2.GameID)
	}
	if r1.Game.Status != StatusWaiting {
		t.Fatalf("rematch should start in waiting, got %s", r1.Game.Status)
	}
	if len(r1.Game.Seats) != len(g.Seats) {
		t.Fatalf("rematch player count must match original")
	}
	if r1.Game.Visibility != g.Visibility {
		t.Fatalf("rematch visibility must match original (got %q vs %q)", r1.Game.Visibility, g.Visibility)
	}
	if r1.GameID == g.ID {
		t.Fatalf("rematch must spawn a *new* game, got the original ID back")
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

func TestResignEndsGameInTwoPlayer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)

	// Alice (seat 0, C1) resigns → Bob (C2) wins.
	dto := postResign(t, ts, g.ID, j1.Token, http.StatusOK)
	if dto.Status != StatusFinished {
		t.Fatalf("want status finished, got %s", dto.Status)
	}
	if dto.Winner != game.C2 {
		t.Fatalf("want winner C2 (Bob), got %v", dto.Winner)
	}
	if dto.WinKind != game.WinResign {
		t.Fatalf("want win kind resign, got %v", dto.WinKind)
	}
}

func TestResignRequiresPlaying(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	// Not started yet (Bob hasn't joined) → resign returns 409.
	postResignRaw(t, ts, g.ID, j1.Token, http.StatusConflict)
}

func TestResignRequiresValidToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	postResignRaw(t, ts, g.ID, "not-a-real-token", http.StatusUnauthorized)
}

func TestDrawOfferAcceptEndsAsDraw(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)

	// Alice offers; the snapshot returned reflects the pending offer.
	dto := postDraw(t, ts, g.ID, "offer", j1.Token, http.StatusOK)
	if dto.DrawOfferBy != 0 {
		t.Fatalf("want drawOfferBy=0 after Alice offered, got %d", dto.DrawOfferBy)
	}
	// Bob accepts → game ends as a draw (Winner=Empty, WinKind=WinDraw).
	dto = postDraw(t, ts, g.ID, "accept", j2.Token, http.StatusOK)
	if dto.Status != StatusFinished {
		t.Fatalf("want status finished, got %s", dto.Status)
	}
	if dto.Winner != game.Empty {
		t.Fatalf("want no winner on draw, got %v", dto.Winner)
	}
	if dto.WinKind != game.WinDraw {
		t.Fatalf("want win kind draw, got %v", dto.WinKind)
	}
	if dto.DrawOfferBy != -1 {
		t.Fatalf("want drawOfferBy cleared after accept, got %d", dto.DrawOfferBy)
	}
}

func TestDrawCannotAcceptOwnOffer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)

	_ = postDraw(t, ts, g.ID, "offer", j1.Token, http.StatusOK)
	postDrawRaw(t, ts, g.ID, "accept", j1.Token, http.StatusConflict)
}

func TestDrawDeclineClearsOffer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)

	_ = postDraw(t, ts, g.ID, "offer", j1.Token, http.StatusOK)
	dto := postDraw(t, ts, g.ID, "decline", j2.Token, http.StatusOK)
	if dto.DrawOfferBy != -1 {
		t.Fatalf("want drawOfferBy cleared after decline, got %d", dto.DrawOfferBy)
	}
	if dto.Status != StatusPlaying {
		t.Fatalf("decline must keep game playing, got %s", dto.Status)
	}
}

func TestDrawAutoCancelsOnMove(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)

	_ = postDraw(t, ts, g.ID, "offer", j1.Token, http.StatusOK)
	// Alice (the offerer) plays a move → the pending offer must be cleared.
	mr := postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	if mr.Game.DrawOfferBy != -1 {
		t.Fatalf("move must auto-cancel pending draw, got drawOfferBy=%d", mr.Game.DrawOfferBy)
	}
}

func TestDrawRejectedInMultiplayer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 3)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	_ = joinGame(t, ts, g.ID, "Charlie", nil)
	postDrawRaw(t, ts, g.ID, "offer", j1.Token, http.StatusConflict)
}

func TestAddBotFillsEmptySeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2) // private by default
	out := postAddBot(t, ts, g.ID, 1, http.StatusOK)
	if !out.Seats[1].IsBot || !out.Seats[1].Occupied {
		t.Fatalf("seat 1 should be bot+occupied, got %+v", out.Seats[1])
	}
	if out.Seats[0].Occupied {
		t.Fatalf("seat 0 must stay empty, got %+v", out.Seats[0])
	}
}

func TestAddBotRejectedOnPublicGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGameWithBody(t, ts, `{"players":2,"visibility":"public"}`, http.StatusCreated)
	postAddBotRaw(t, ts, g.ID, 1, http.StatusConflict)
}

func TestAddBotRejectedOnTakenSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	postAddBotRaw(t, ts, g.ID, 0, http.StatusConflict)
}

func TestAddBotStartsGameWhenLastSeatFilled(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	out := postAddBot(t, ts, g.ID, 1, http.StatusOK)
	if out.Status != StatusPlaying {
		t.Fatalf("want status playing after bot fills last seat, got %s", out.Status)
	}
	// And the bot should eventually play if Alice (seat 0) doesn't move.
	// We don't assert that here — covered by TestBotPlaysWhenItIsItsTurn.
}

func TestBotPlaysWhenItIsItsTurn(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j := joinGame(t, ts, g.ID, "Alice", nil)
	_ = postAddBot(t, ts, g.ID, 1, http.StatusOK)

	mr := postMove(t, ts, g.ID, j.Token, 0, 0, http.StatusOK)
	if mr.Game.MoveCount != 1 {
		t.Fatalf("right after human's move want moveCount=1, got %d", mr.Game.MoveCount)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		cur := getGameViaHTTP(t, ts, g.ID)
		if cur.MoveCount >= 2 && cur.Turn == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bot never played; moveCount=%d turn=%d", cur.MoveCount, cur.Turn)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMatchmakeRejectsAnonymous(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// No X-Test-User-ID header → 401.
	resp, _ := http.Post(ts.URL+"/api/games/matchmake", "application/json", strings.NewReader(`{"players":2}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestMatchmakeCreatesAndAutoJoins(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	j := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	if j.Game.Status != StatusWaiting {
		t.Fatalf("want waiting, got %s", j.Game.Status)
	}
	if j.Game.Visibility != VisibilityPublic {
		t.Fatalf("matchmade games must be public, got %q", j.Game.Visibility)
	}
	if !j.Game.Seats[0].Occupied {
		t.Fatalf("matchmake should auto-seat the caller, got seat 0 = %+v", j.Game.Seats[0])
	}
	if j.Token == "" {
		t.Fatalf("matchmake must return a seat token")
	}
}

func TestMatchmakePairsTwoCallers(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	alice := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	bob := postMatchmake(t, ts, 2, "bob", http.StatusOK)
	if alice.Game.ID != bob.Game.ID {
		t.Fatalf("two callers for 2P should land in the same game; got %s and %s", alice.Game.ID, bob.Game.ID)
	}
	if bob.Game.Status != StatusPlaying {
		t.Fatalf("game should transition to playing once both seats are filled, got %s", bob.Game.Status)
	}
}

func TestMatchmakeSkipsGameWhereUserAlreadySeated(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	first := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	// Alice clicks 1v1 again — she's already seated in `first`, so the
	// matchmaker must create a new public game rather than handing her
	// back the one she's already in.
	second := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	if second.Game.ID == first.Game.ID {
		t.Fatalf("matchmake should not return a game the caller is already seated in")
	}
}

func TestMatchmakeSkipsFullGames(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	a := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	b := postMatchmake(t, ts, 2, "bob", http.StatusOK)
	if a.Game.ID != b.Game.ID || b.Game.Status != StatusPlaying {
		t.Fatalf("setup broke: a.id=%s b.id=%s status=%s", a.Game.ID, b.Game.ID, b.Game.Status)
	}
	// Now a third caller — the game above is full/playing, so matchmake
	// must spawn a fresh one.
	c := postMatchmake(t, ts, 2, "carol", http.StatusOK)
	if c.Game.ID == a.Game.ID {
		t.Fatalf("matchmake should not return a full game")
	}
}

func TestMatchmakeSeparatesPlayerCounts(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	two := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	four := postMatchmake(t, ts, 4, "alice", http.StatusOK)
	if two.Game.ID == four.Game.ID {
		t.Fatalf("2-player and 4-player matchmaking must return different games")
	}
	if len(two.Game.Seats) != 2 || len(four.Game.Seats) != 4 {
		t.Fatalf("seat counts mismatched (2P=%d 4P=%d)", len(two.Game.Seats), len(four.Game.Seats))
	}
}

func TestLeaveSeatFreesIt(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	j := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	// Alice cancels → her seat is freed.
	resp, err := postLeave(t, ts, j.Game.ID, j.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("leave: status=%d", resp.StatusCode)
	}
	cur := getGameViaHTTP(t, ts, j.Game.ID)
	if cur.Seats[j.Seat.Index].Occupied {
		t.Fatalf("seat should be empty after leave, got %+v", cur.Seats[j.Seat.Index])
	}
}

func TestLeaveSeatRejectedAfterStart(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	a := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	_ = postMatchmake(t, ts, 2, "bob", http.StatusOK) // fills the game → playing
	resp, _ := postLeave(t, ts, a.Game.ID, a.Token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 leaving a started game, got %d", resp.StatusCode)
	}
}

func TestLeaderboardEmptyByDefault(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/leaderboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var out []LeaderboardEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("hermetic server has no DB → empty leaderboard, got %d entries", len(out))
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

func createGameWithBody(t *testing.T, ts *httptest.Server, body string, wantStatus int) gameDTO {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/games", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: want status=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusCreated {
		return gameDTO{}
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func postAddBot(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postAddBotRaw(t, ts, gameID, seatIdx, wantStatus))
}

func postAddBotRaw(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) *http.Response {
	t.Helper()
	url := ts.URL + "/api/games/" + gameID + "/seats/" + itoa(seatIdx) + "/bot"
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("addBot: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

func postLeave(t *testing.T, ts *httptest.Server, gameID, token string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/leave", nil)
	req.Header.Set("X-Player-Token", token)
	return http.DefaultClient.Do(req)
}

// postMatchmake calls /api/games/matchmake as `userID`, which the hermetic
// auth middleware turns into an AuthUser via the X-Test-User-ID back door.
// Returns the joinResponse — the caller is automatically seated.
func postMatchmake(t *testing.T, ts *httptest.Server, players int, userID string, wantStatus int) joinResponse {
	t.Helper()
	body := strings.NewReader(`{"players":` + itoa(players) + `}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/matchmake", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("matchmake: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusOK {
		return joinResponse{}
	}
	var j joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	return j
}

func postRematch(t *testing.T, ts *httptest.Server, id string, wantStatus int) rematchResponse {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/games/"+id+"/rematch", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("rematch: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusCreated {
		return rematchResponse{}
	}
	var r rematchResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

// finishGame plays an 11-move sequence in `gameID` that lands a 6-alignment
// for Alice on the q-axis (instant win, regardless of config thresholds).
// Bob plays parallel one row up, so neither side ever sandwiches the other —
// no captures interfere.
func finishGame(t *testing.T, ts *httptest.Server, gameID, aliceTok, bobTok string) {
	t.Helper()
	for q := 0; q < 5; q++ {
		_ = postMove(t, ts, gameID, aliceTok, q, 0, http.StatusOK)
		_ = postMove(t, ts, gameID, bobTok, q, 1, http.StatusOK)
	}
	// Final Alice stone completes the 6-alignment.
	mr := postMove(t, ts, gameID, aliceTok, 5, 0, http.StatusOK)
	if mr.Game.Status != StatusFinished {
		t.Fatalf("finishGame: game still %s after 6-alignment", mr.Game.Status)
	}
}

func postResign(t *testing.T, ts *httptest.Server, gameID, token string, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postResignRaw(t, ts, gameID, token, wantStatus))
}

func postResignRaw(t *testing.T, ts *httptest.Server, gameID, token string, wantStatus int) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/resign", nil)
	req.Header.Set("X-Player-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("resign: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

func postDraw(t *testing.T, ts *httptest.Server, gameID, op, token string, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postDrawRaw(t, ts, gameID, op, token, wantStatus))
}

func postDrawRaw(t *testing.T, ts *httptest.Server, gameID, op, token string, wantStatus int) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/draw/"+op, nil)
	req.Header.Set("X-Player-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("draw/%s: want=%d got=%d body=%s", op, wantStatus, resp.StatusCode, b)
	}
	return resp
}

func getGameViaHTTP(t *testing.T, ts *httptest.Server, gameID string) gameDTO {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/games/" + gameID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get game: %d", resp.StatusCode)
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func decodeGameDTO(t *testing.T, resp *http.Response) gameDTO {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return gameDTO{}
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
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

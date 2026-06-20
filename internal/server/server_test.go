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

	"github.com/alexis-marcel/gemline/internal/game"
	"github.com/coder/websocket"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Bots play instantly so assertions don't have to sleep.
	store := NewStore(nil).WithBotDelay(0)
	srv, err := New(log, store, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return httptest.NewServer(srv.Routes())
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

func TestCreateGameRejectsPublicVisibility(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// Public games come only from matchmaking; create-as-public would let
	// anyone open a permanent public room.
	_ = createGameWithBody(t, ts, `{"players":2,"visibility":"public","name":"Host"}`, http.StatusBadRequest)
}

func TestCreateGameRejectsBadVisibility(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	_ = createGameWithBody(t, ts, `{"players":2,"visibility":"unicorn","name":"Host"}`, http.StatusBadRequest)
}

func TestCreateGameRejectsAnonymousWithoutName(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// No JWT and no name → can't auto-join the creator anywhere.
	_ = createGameWithBody(t, ts, `{"players":2}`, http.StatusBadRequest)
}

func TestRematchOfferRequiresFinishedGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	// Still waiting → /rematch/offer must 409 even with a valid token.
	_ = postRematchOffer(t, ts, g.ID, j1.Token, http.StatusConflict)
}

func TestRematchOfferUnanimous(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)

	// Alice proposes — offer created, no rematch game yet.
	d1 := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d1.RematchGameID != "" {
		t.Fatalf("rematch should not exist yet after one acceptance, got %q", d1.RematchGameID)
	}
	if d1.RematchOffer == nil || len(d1.RematchOffer.PendingSeats) != 1 || d1.RematchOffer.PendingSeats[0] != 1 {
		t.Fatalf("want Bob (seat 1) pending after Alice accepts, got %#v", d1.RematchOffer)
	}
	// Alice re-clicking is idempotent.
	d1b := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d1b.RematchGameID != "" {
		t.Fatalf("re-accept by Alice must not create rematch, got %q", d1b.RematchGameID)
	}
	// Bob accepts → rematch created under a new ID.
	d2 := postRematchOffer(t, ts, g.ID, j2.Token, http.StatusOK)
	if d2.RematchGameID == "" {
		t.Fatalf("rematch must be created once both seats have accepted")
	}
	if d2.RematchGameID == g.ID {
		t.Fatalf("rematch must spawn a *new* game, got the original ID back")
	}
	// Repeat accepts resolve to the same rematch.
	d3 := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d3.RematchGameID != d2.RematchGameID {
		t.Fatalf("rematch ID must stay stable on repeat accept; got %s then %s", d2.RematchGameID, d3.RematchGameID)
	}
	rematch := getGameViaHTTP(t, ts, d2.RematchGameID)
	if rematch.Status != StatusWaiting {
		t.Fatalf("rematch should start in waiting, got %s", rematch.Status)
	}
	if len(rematch.Seats) != len(g.Seats) {
		t.Fatalf("rematch player count must match original")
	}
	if rematch.Visibility != g.Visibility {
		t.Fatalf("rematch visibility must match original (got %q vs %q)", rematch.Visibility, g.Visibility)
	}
}

func TestRematchOfferDecline(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)

	_ = postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	d := postRematchDecline(t, ts, g.ID, j2.Token, http.StatusOK)
	if d.RematchOffer != nil {
		t.Fatalf("offer should be cleared after decline, got %#v", d.RematchOffer)
	}
	if d.RematchGameID != "" {
		t.Fatalf("decline must not create a rematch, got %q", d.RematchGameID)
	}
	// Declining with no pending offer → 409.
	_ = postRematchDecline(t, ts, g.ID, j1.Token, http.StatusConflict)
}

// TestRematchOffer_RequiresToken: the propose/accept path authenticates via the
// seat token, not a JWT, so a missing X-Player-Token must 401.
func TestRematchOffer_RequiresToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)
	_ = postRematchOffer(t, ts, g.ID, "", http.StatusUnauthorized)
}

// TestRematchOffer_ThreePlayers_NeedsAllThreeAccepts: with three humans, the
// rematch fires only after all accept; pendingSeats shrinks each step.
func TestRematchOffer_ThreePlayers_NeedsAllThreeAccepts(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 3)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	b := joinGame(t, ts, g.ID, "Bob", nil)
	c := joinGame(t, ts, g.ID, "Carol", nil)
	// One resign finishes a 3P game.
	postResign(t, ts, g.ID, a.Token, http.StatusOK)

	d1 := postRematchOffer(t, ts, g.ID, a.Token, http.StatusOK)
	if d1.RematchGameID != "" {
		t.Fatalf("rematch must not exist after only 1/3 accepts")
	}
	if d1.RematchOffer == nil || len(d1.RematchOffer.PendingSeats) != 2 {
		t.Fatalf("want 2 pending after Alice, got %#v", d1.RematchOffer)
	}
	d2 := postRematchOffer(t, ts, g.ID, b.Token, http.StatusOK)
	if d2.RematchGameID != "" {
		t.Fatalf("rematch must not exist after 2/3 accepts")
	}
	if d2.RematchOffer == nil || len(d2.RematchOffer.PendingSeats) != 1 {
		t.Fatalf("want 1 pending after Alice+Bob, got %#v", d2.RematchOffer)
	}
	d3 := postRematchOffer(t, ts, g.ID, c.Token, http.StatusOK)
	if d3.RematchGameID == "" {
		t.Fatalf("rematch must exist once all 3 humans have accepted")
	}
}

// TestRematchOffer_BotIsPreAccepted: with a bot at the table, only the human
// clicks "Revanche" — the bot's acceptance is implicit.
func TestRematchOffer_BotIsPreAccepted(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	_ = postAddBot(t, ts, g.ID, 1, http.StatusOK)
	postResign(t, ts, g.ID, a.Token, http.StatusOK)

	d := postRematchOffer(t, ts, g.ID, a.Token, http.StatusOK)
	if d.RematchGameID == "" {
		t.Fatalf("rematch must be created on Alice's single accept (bot pre-accepted), got %#v", d)
	}
	// The bot carries forward on its original seat so "play vs bot again"
	// doesn't re-prompt the host.
	rematch := getGameViaHTTP(t, ts, d.RematchGameID)
	if !rematch.Seats[1].Occupied || !rematch.Seats[1].IsBot {
		t.Fatalf("bot must be pre-seated in the rematch on its original seat, got %+v", rematch.Seats[1])
	}
	// Alice is anon, so we can't deliver her a token: her seat stays empty and
	// the rematch waits until she re-joins client-side. (Authed counterpart:
	// TestRematchOffer_AuthedHumansArePreSeated.)
	if rematch.Seats[0].Occupied {
		t.Fatalf("anon human must not be pre-seated, got %+v", rematch.Seats[0])
	}
	if rematch.Status != StatusWaiting {
		t.Fatalf("rematch with an empty anon seat must stay waiting, got %s", rematch.Status)
	}
}

// TestRematchOffer_AuthedHumansArePreSeated: the rematch reserves the same
// UserIDs at the same seats (no token yet — clients resolve it on arrival) and
// starts in `playing` since every seat is filled — no "Recherche" flash.
func TestRematchOffer_AuthedHumansArePreSeated(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGameAs(t, ts, g.ID, "Alice", "alice-uuid", nil)
	b := joinGameAs(t, ts, g.ID, "Bob", "bob-uuid", nil)
	finishGame(t, ts, g.ID, a.Token, b.Token)

	_ = postRematchOffer(t, ts, g.ID, a.Token, http.StatusOK)
	d := postRematchOffer(t, ts, g.ID, b.Token, http.StatusOK)
	if d.RematchGameID == "" {
		t.Fatalf("rematch must be created after both accept")
	}

	rematch := getGameViaHTTP(t, ts, d.RematchGameID)
	if rematch.Status != StatusPlaying {
		t.Fatalf("rematch with all seats pre-filled must start in playing, got %s", rematch.Status)
	}
	if !rematch.Seats[0].Occupied || rematch.Seats[0].UserID != "alice-uuid" {
		t.Fatalf("seat 0 must carry Alice's UserID, got %+v", rematch.Seats[0])
	}
	if !rematch.Seats[1].Occupied || rematch.Seats[1].UserID != "bob-uuid" {
		t.Fatalf("seat 1 must carry Bob's UserID, got %+v", rematch.Seats[1])
	}
}

// TestRateLimit_Enqueue: bursting past the enqueue limit yields a 429.
func TestRateLimit_Enqueue(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	got429 := false
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/matchmake/enqueue", strings.NewReader(`{"players":2}`))
		req.Header.Set("X-Test-User-ID", "rl-user")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a 429 after exceeding the enqueue rate limit")
	}
}

// TestResolveSeat_PreSeatedHumanPullsCreds: a pre-seated authed human (no token
// until they ask) pulls their seat token over HTTP by JWT, and that token
// authorises play. This is the single creds channel for matchmaking and rematch.
func TestResolveSeat_PreSeatedHumanPullsCreds(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGameAs(t, ts, g.ID, "Alice", "alice-uuid", nil)
	b := joinGameAs(t, ts, g.ID, "Bob", "bob-uuid", nil)
	finishGame(t, ts, g.ID, a.Token, b.Token)

	_ = postRematchOffer(t, ts, g.ID, a.Token, http.StatusOK)
	d := postRematchOffer(t, ts, g.ID, b.Token, http.StatusOK)
	if d.RematchGameID == "" {
		t.Fatalf("rematch must be created after both accept")
	}

	resolved := postResolveSeatAs(t, ts, d.RematchGameID, "alice-uuid", http.StatusOK)
	if resolved.Seat.Index != 0 || resolved.Token == "" {
		t.Fatalf("resolve must return Alice's seat 0 with a token, got %+v", resolved)
	}
	// The issued token must authorise Alice's opening move in the rematch.
	_ = postMove(t, ts, d.RematchGameID, resolved.Token, 0, 0, http.StatusOK)

	// A user with no seat in the game is refused.
	_ = postResolveSeatAs(t, ts, d.RematchGameID, "carol-uuid", http.StatusForbidden)
}

// TestCurrentMatchmade_Contract: the durable match-poll endpoint requires auth
// and reports no match as {"gameId":""} (the noop store has no queue).
func TestCurrentMatchmade_Contract(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// No JWT → 401.
	resp, err := http.Get(ts.URL + "/api/matchmake/current")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated poll must be 401, got %d", resp.StatusCode)
	}

	// Authed, no match → 200 {"gameId":""}.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/matchmake/current", nil)
	req.Header.Set("X-Test-User-ID", "alice-uuid")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed poll must be 200, got %d", resp.StatusCode)
	}
	var body struct {
		GameID string `json:"gameId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.GameID != "" {
		t.Fatalf("no match expected, got gameId=%q", body.GameID)
	}
}

func postResolveSeatAs(t *testing.T, ts *httptest.Server, gameID, userID string, wantStatus int) joinResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/seat/resolve", nil)
	req.Header.Set("X-Test-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("resolveSeat: status=%d (want %d) body=%s", resp.StatusCode, wantStatus, body)
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

func TestLastMove_PopulatedOnDTOAfterMove(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	if g.LastMove != nil {
		t.Fatalf("fresh game must have no lastMove, got %+v", g.LastMove)
	}
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	mr := postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	if mr.Game.LastMove == nil {
		t.Fatalf("after a move, lastMove must be set")
	}
	if mr.Game.LastMove.Q != 0 || mr.Game.LastMove.R != 0 {
		t.Fatalf("lastMove must point at the played coord, got %+v", mr.Game.LastMove)
	}
	// The GET endpoint reuses toGameDTO, so verify lastMove round-trips there too.
	again := getGameViaHTTP(t, ts, g.ID)
	if again.LastMove == nil || again.LastMove.Q != 0 || again.LastMove.R != 0 {
		t.Fatalf("HTTP get must surface lastMove identically, got %+v", again.LastMove)
	}
}

func TestDeclineSeatInvite_HappyPath(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	const aliceID = "alice-uuid"
	_ = postInviteSeat(t, ts, g.ID, 1, aliceID, "Alice", http.StatusOK)
	out := postDeclineInviteAs(t, ts, g.ID, 1, aliceID, http.StatusOK)
	if out.Seats[1].Occupied || out.Seats[1].Name != "" || out.Seats[1].UserID != "" {
		t.Fatalf("decline must reset the seat, got %+v", out.Seats[1])
	}
}

func TestDeclineSeatInvite_RejectsNonInvitee(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	// Bob declining Alice's invite → 403.
	_ = postDeclineInviteAs(t, ts, g.ID, 1, "bob-uuid", http.StatusForbidden)
}

func TestDeclineSeatInvite_RejectsEmptySeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	// No invitation on seat 1 → ErrSeatNotInvited → 409.
	_ = postDeclineInviteAs(t, ts, g.ID, 1, "alice-uuid", http.StatusConflict)
}

// TestInviteSeat_PushesInviteReceivedOverLobbyWS: the host's invite must reach
// the invitee's persistent lobby WS as `invite_received` so the global toast
// renders even when the invitee isn't on the game page yet.
func TestInviteSeat_PushesInviteReceivedOverLobbyWS(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const aliceID = "alice-uuid"
	g := createGame(t, ts, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/lobby"
	hdr := http.Header{}
	hdr.Set("X-Test-User-ID", aliceID)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	// Fire the invite from another goroutine; we block on Read below.
	go func() {
		_ = postInviteSeat(t, ts, g.ID, 1, aliceID, "Alice", http.StatusOK)
	}()

	ev := readEvent(t, ctx, conn)
	if ev.Type != "invite_received" {
		t.Fatalf("want invite_received, got %s", ev.Type)
	}
	// FromName is unasserted: the inviter is anonymous in the hermetic test.
	raw, ok := ev.Payload.(map[string]any)
	if !ok {
		t.Fatalf("want object payload, got %T", ev.Payload)
	}
	if got, _ := raw["gameId"].(string); got != g.ID {
		t.Fatalf("want gameId=%s, got %v", g.ID, raw["gameId"])
	}
	if got, _ := raw["seatIndex"].(float64); int(got) != 1 {
		t.Fatalf("want seatIndex=1, got %v", raw["seatIndex"])
	}
}

// TestCancelSeatInvite_PushesInviteCancelledOverLobbyWS: cancelling an invite
// must dismiss the invitee's toast via an `invite_cancelled` push.
func TestCancelSeatInvite_PushesInviteCancelledOverLobbyWS(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const aliceID = "alice-uuid"
	g := createGame(t, ts, 2)
	// Reserve the seat before opening the WS so the invitee is subscribed.
	_ = postInviteSeat(t, ts, g.ID, 1, aliceID, "Alice", http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/lobby"
	hdr := http.Header{}
	hdr.Set("X-Test-User-ID", aliceID)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	go func() {
		postCancelSeatInvite(t, ts, g.ID, 1, http.StatusOK)
	}()

	ev := readEvent(t, ctx, conn)
	if ev.Type != "invite_cancelled" {
		t.Fatalf("want invite_cancelled, got %s", ev.Type)
	}
}

func TestDeclineSeatInvite_RequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	// No X-Test-User-ID → unauthenticated → 401 before the store.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+g.ID+"/seats/1/invite/decline", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 without auth, got %d", resp.StatusCode)
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

	// Alice (seat 0) plays first.
	mr := postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	if mr.Game.Turn != 1 {
		t.Fatalf("want turn=1 after first move, got %d", mr.Game.Turn)
	}
	// Bob plays Alice's occupied cell → rejected.
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
	// Alice plays, then plays again out of turn → ErrWrongTurn.
	_ = postMove(t, ts, g.ID, j1.Token, 0, 0, http.StatusOK)
	_ = postMove(t, ts, g.ID, j1.Token, 1, 0, http.StatusBadRequest)
}

func TestJoinRejectsFullGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	// Third join on a 2-player game must 409.
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

	// First message is the initial state snapshot.
	first := readEvent(t, ctx, conn)
	if first.Type != "state" {
		t.Fatalf("want first event type=state, got %s", first.Type)
	}

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
	// Not started (Bob hasn't joined) → resign 409.
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

	dto := postDraw(t, ts, g.ID, "offer", j1.Token, http.StatusOK)
	if dto.DrawOfferBy != 0 {
		t.Fatalf("want drawOfferBy=0 after Alice offered, got %d", dto.DrawOfferBy)
	}
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
	// The offerer playing a move must auto-cancel the pending offer.
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
	g := createGame(t, ts, 2)
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
	// Public games only come from matchmaking now.
	pub := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	postAddBotRaw(t, ts, pub.Game.ID, 1, http.StatusConflict)
}

func TestAddBotRejectedOnTakenSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	postAddBotRaw(t, ts, g.ID, 0, http.StatusConflict)
}

func TestInviteSeat_FillsSeatAsReserved(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	out := postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	if out.Seats[1].Occupied || out.Seats[1].IsBot {
		t.Fatalf("invited seat must stay unoccupied + non-bot, got %+v", out.Seats[1])
	}
	if out.Seats[1].Name != "Alice" {
		t.Fatalf("invited seat must take inviteeName, got %q", out.Seats[1].Name)
	}
	if out.Status != StatusWaiting {
		t.Fatalf("invite must NOT promote the game to playing (occupied stays false), got %s", out.Status)
	}
}

func TestInviteSeat_RejectedOnPublicGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	pub := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	postInviteSeatRaw(t, ts, pub.Game.ID, 1, "bob-uuid", "Bob", http.StatusConflict)
}

func TestInviteSeat_RejectedOnTakenSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	postInviteSeatRaw(t, ts, g.ID, 0, "anyone-uuid", "Anyone", http.StatusConflict)
}

func TestCancelSeatInvite_FreesSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	out := postCancelSeatInvite(t, ts, g.ID, 1, http.StatusOK)
	if out.Seats[1].Occupied || out.Seats[1].Name != "" {
		t.Fatalf("cancel must reset seat, got %+v", out.Seats[1])
	}
}

func TestCancelSeatInvite_RejectedOnHumanSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	// Alice is a real player, not an invite — cancel must refuse, not evict her.
	postCancelSeatInviteRaw(t, ts, g.ID, 0, http.StatusConflict)
}

func TestJoin_InvitedUserGetsReservedSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	// Auto-pick join: the reserved seat 1 wins over the empty seat 0.
	j := joinGameAs(t, ts, g.ID, "Alice", "alice-uuid", nil)
	if j.Seat.Index != 1 {
		t.Fatalf("invited user must land on their reserved seat (1), got %d", j.Seat.Index)
	}
}

func TestJoin_OtherUserCannotClaimReservedSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	// Bob explicitly asks for Alice's reserved seat 1 → 403.
	one := 1
	body, _ := json.Marshal(map[string]any{"name": "Bob", "seat": one})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+g.ID+"/join", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User-ID", "bob-uuid")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403 forbidden on claiming reserved seat, got %d", resp.StatusCode)
	}
}

func TestRemoveBotFreesSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = postAddBot(t, ts, g.ID, 1, http.StatusOK)
	out := postRemoveBot(t, ts, g.ID, 1, http.StatusOK)
	if out.Seats[1].Occupied || out.Seats[1].IsBot {
		t.Fatalf("seat 1 must be empty after removeBot, got %+v", out.Seats[1])
	}
}

func TestRemoveBotRejectedOnHumanSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	// Seat 0 is human, not a bot → removeBot 409.
	postRemoveBotRaw(t, ts, g.ID, 0, http.StatusConflict)
}

func TestRemoveBotRejectedOnEmptySeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	postRemoveBotRaw(t, ts, g.ID, 1, http.StatusConflict)
}

func TestRemoveBotRejectedAfterStart(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	out := postAddBot(t, ts, g.ID, 1, http.StatusOK)
	if out.Status != StatusPlaying {
		t.Fatalf("test setup: expected playing after bot fill, got %s", out.Status)
	}
	postRemoveBotRaw(t, ts, g.ID, 1, http.StatusConflict)
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
	resp, err := http.Post(ts.URL+"/api/games/matchmake", "application/json", strings.NewReader(`{"players":2}`))
	if err != nil {
		t.Fatal(err)
	}
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
	// Alice clicks again: already seated in `first`, so she must get a new game.
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
	// The game above is full/playing, so a third caller must get a fresh one.
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

func TestStartTrimsToOccupiedSeats(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	b := joinGame(t, ts, g.ID, "Bob", nil)
	if a.Game.Status != StatusWaiting || b.Game.Status != StatusWaiting {
		t.Fatalf("game must stay waiting before Start (status=%s)", b.Game.Status)
	}
	out := postStart(t, ts, g.ID, a.Token, http.StatusOK)
	if out.Status != StatusPlaying {
		t.Fatalf("want playing after Start, got %s", out.Status)
	}
	// Start trims the 6 seats down to the 2 that were occupied.
	if len(out.Seats) != 2 {
		t.Fatalf("want 2 seats after Start (trim of 6→2), got %d", len(out.Seats))
	}
	for i, seat := range out.Seats {
		if !seat.Occupied || seat.IsBot {
			t.Fatalf("seat %d should be an occupied human after Start: %+v", i, seat)
		}
	}
	if len(out.Players) != 2 {
		t.Fatalf("engine players must match seat count after trim, got %d", len(out.Players))
	}
}

// TestStartRecomputesThresholdsForOccupiedCount: a 6-slot game started with 3
// occupied seats plays under the 3-player rulebook. Thresholds are picked at
// Start, not Create.
func TestStartRecomputesThresholdsForOccupiedCount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	joinGame(t, ts, g.ID, "Bob", nil)
	joinGame(t, ts, g.ID, "Carol", nil)
	out := postStart(t, ts, g.ID, a.Token, http.StatusOK)
	want3 := game.DefaultConfig(3)
	if out.Thresholds.Align4ToWin != want3.Align4ToWin ||
		out.Thresholds.Align5ToWin != want3.Align5ToWin ||
		out.Thresholds.CapturePairsWin != want3.CapturePairsWin {
		t.Fatalf("started with 3/6 seats: want 3-player thresholds %+v, got %+v",
			want3, out.Thresholds)
	}
}

// TestWaitingPreviewsThresholdsByOccupiedCount: while waiting, the previewed
// thresholds reflect current occupancy (what the lobby UI binds to), not the
// slot count — falling back to 2-player rules at 0 seated.
func TestWaitingPreviewsThresholdsByOccupiedCount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	want2 := game.DefaultConfig(2)
	if g.Thresholds.Align4ToWin != want2.Align4ToWin ||
		g.Thresholds.Align5ToWin != want2.Align5ToWin {
		t.Fatalf("0 seated → preview should fall back to 2-player rules; got %+v", g.Thresholds)
	}
	joinGame(t, ts, g.ID, "Alice", nil)
	joinGame(t, ts, g.ID, "Bob", nil)
	c := joinGame(t, ts, g.ID, "Carol", nil)
	want3 := game.DefaultConfig(3)
	if c.Game.Thresholds.Align4ToWin != want3.Align4ToWin ||
		c.Game.Thresholds.Align5ToWin != want3.Align5ToWin ||
		c.Game.Thresholds.CapturePairsWin != want3.CapturePairsWin {
		t.Fatalf("3 seated → preview should reflect 3-player rules; got %+v vs want %+v",
			c.Game.Thresholds, want3)
	}
}

func TestStartPreservesUserColors(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	// Non-contiguous seats (0 and 4) so the trim has gaps to close.
	zero := 0
	four := 4
	a := joinGame(t, ts, g.ID, "Alice", &zero)
	b := joinGame(t, ts, g.ID, "Bob", &four)
	out := postStart(t, ts, g.ID, a.Token, http.StatusOK)
	// Trim re-orders but preserves colours: Alice keeps C1, Bob keeps C5.
	colors := []game.Color{out.Seats[0].Color, out.Seats[1].Color}
	if colors[0] != a.Seat.Color || colors[1] != b.Seat.Color {
		t.Fatalf("trim should preserve original colours, got %v (wanted Alice=%v, Bob=%v)",
			colors, a.Seat.Color, b.Seat.Color)
	}
}

func TestStart_OnlyHostMayStart(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 4)
	zero := 0
	one := 1
	host := joinGame(t, ts, g.ID, "Host", &zero)
	guest := joinGame(t, ts, g.ID, "Guest", &one)

	// Only the host (seat 0) may Start; a guest's valid token must 403.
	postStartRaw(t, ts, g.ID, guest.Token, http.StatusForbidden)
	_ = postStart(t, ts, g.ID, host.Token, http.StatusOK)
}

func TestStartRejectsWithFewerThanTwo(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	postStartRaw(t, ts, g.ID, a.Token, http.StatusConflict)
}

func TestStartRejectsOnPublic(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// Public games auto-start via matchmaking; manual Start must 409.
	pub := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	postStartRaw(t, ts, pub.Game.ID, pub.Token, http.StatusConflict)
}

func TestStartRejectsWithoutToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6)
	_ = joinGame(t, ts, g.ID, "Alice", nil)
	_ = joinGame(t, ts, g.ID, "Bob", nil)
	postStartRaw(t, ts, g.ID, "", http.StatusUnauthorized)
}

func TestJoinPublicRejectsAnonymous(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// The public game's URL is reachable by anyone with the ID; anonymous join
	// must 401 so it can't bypass the matchmaking-only contract.
	pub := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	body := bytes.NewBufferString(`{"name":"Bob"}`)
	resp, err := http.Post(ts.URL+"/api/games/"+pub.Game.ID+"/join", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 on anonymous join to public game, got %d", resp.StatusCode)
	}
}

func TestJoinPrivateStillAllowsAnonymous(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// Private games must still accept anonymous joins for URL-share play.
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Anon", nil)
}

func TestLeaveSeatRejectedAfterStart(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	a := postMatchmake(t, ts, 2, "alice", http.StatusOK)
	_ = postMatchmake(t, ts, 2, "bob", http.StatusOK) // fills → playing
	resp, _ := postLeave(t, ts, a.Game.ID, a.Token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 leaving a started game, got %d", resp.StatusCode)
	}
}

// TestGameRatings_NoopFallsBackToUnrated: in hermetic mode (no DB) the ratings
// endpoint reports rated:false with empty seats so the client can render a
// generic end-of-game card.
func TestGameRatings_NoopFallsBackToUnrated(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)

	resp, err := http.Get(ts.URL + "/api/games/" + g.ID + "/ratings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, b)
	}
	var gr GameRatings
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		t.Fatal(err)
	}
	if gr.Rated {
		t.Fatalf("noop repo must report rated:false, got %+v", gr)
	}
	if len(gr.Seats) != 0 {
		t.Fatalf("noop repo must report empty seats, got %+v", gr.Seats)
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

// TestGetMe_AutoFillsDisplayName: a user who never set a display name must still
// get a usable one from /api/auth/me, derived from their email. The pre-fix bug
// returned {displayName: ""} and never created the profile row.
func TestGetMe_AutoFillsDisplayName(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/auth/me", nil)
	// X-Test-User-ID is the hermetic auth back door → AuthUser{ID,
	// Email: id+"@test.local"} when no JWT verifier is configured.
	req.Header.Set("X-Test-User-ID", "11111111-1111-1111-1111-111111111111")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var dto ProfileDTO
	if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	// Non-empty proves the handler builds a synthetic Profile (fallback derives
	// it from the email) since the noop repo persists nothing.
	if dto.DisplayName == "" {
		t.Fatalf("displayName must be auto-filled even when no profile row exists; got %+v", dto)
	}
}

// ---- helpers ----

// createGame returns a private game with every seat empty. The HTTP endpoint
// auto-joins the caller, so we create-then-leave to hand back a "fresh" game.
func createGame(t *testing.T, ts *httptest.Server, players int) gameDTO {
	t.Helper()
	body := strings.NewReader(`{"players":` + itoa(players) + `,"name":"__seed__"}`)
	resp, err := http.Post(ts.URL+"/api/games", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: status=%d body=%s", resp.StatusCode, b)
	}
	var j joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	leaveResp, err := postLeave(t, ts, j.Game.ID, j.Token)
	if err != nil {
		t.Fatal(err)
	}
	leaveResp.Body.Close()
	return getGameViaHTTP(t, ts, j.Game.ID)
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

func postRemoveBot(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postRemoveBotRaw(t, ts, gameID, seatIdx, wantStatus))
}

func postRemoveBotRaw(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) *http.Response {
	t.Helper()
	url := ts.URL + "/api/games/" + gameID + "/seats/" + itoa(seatIdx) + "/bot"
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("removeBot: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

func postInviteSeat(t *testing.T, ts *httptest.Server, gameID string, seatIdx int, userID, name string, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postInviteSeatRaw(t, ts, gameID, seatIdx, userID, name, wantStatus))
}

func postInviteSeatRaw(t *testing.T, ts *httptest.Server, gameID string, seatIdx int, userID, name string, wantStatus int) *http.Response {
	t.Helper()
	url := ts.URL + "/api/games/" + gameID + "/seats/" + itoa(seatIdx) + "/invite"
	body, _ := json.Marshal(map[string]string{"userId": userID, "displayName": name})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("inviteSeat: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

func postCancelSeatInvite(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postCancelSeatInviteRaw(t, ts, gameID, seatIdx, wantStatus))
}

func postCancelSeatInviteRaw(t *testing.T, ts *httptest.Server, gameID string, seatIdx, wantStatus int) *http.Response {
	t.Helper()
	url := ts.URL + "/api/games/" + gameID + "/seats/" + itoa(seatIdx) + "/invite"
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("cancelInviteSeat: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

// joinGameAs is the authenticated variant of joinGame, sending X-Test-User-ID
// so the join is treated as a logged-in user (needed for reserved-seat tests).
func joinGameAs(t *testing.T, ts *httptest.Server, id, name, userID string, seat *int) joinResponse {
	t.Helper()
	payload := map[string]any{"name": name}
	if seat != nil {
		payload["seat"] = *seat
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+id+"/join", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("joinAs: status=%d body=%s", resp.StatusCode, body)
	}
	var j joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	return j
}

func postStart(t *testing.T, ts *httptest.Server, gameID, token string, wantStatus int) gameDTO {
	t.Helper()
	return decodeGameDTO(t, postStartRaw(t, ts, gameID, token, wantStatus))
}

func postStartRaw(t *testing.T, ts *httptest.Server, gameID, token string, wantStatus int) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/start", nil)
	if token != "" {
		req.Header.Set("X-Player-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("start: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	return resp
}

func postLeave(t *testing.T, ts *httptest.Server, gameID, token string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/leave", nil)
	req.Header.Set("X-Player-Token", token)
	return http.DefaultClient.Do(req)
}

// postMatchmake calls /api/games/matchmake as userID (via the X-Test-User-ID
// back door); the caller is auto-seated in the returned joinResponse.
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

func postRematchOffer(t *testing.T, ts *httptest.Server, id, token string, wantStatus int) gameDTO {
	t.Helper()
	return postWithToken(t, ts, "/api/games/"+id+"/rematch/offer", token, wantStatus)
}

func postRematchDecline(t *testing.T, ts *httptest.Server, id, token string, wantStatus int) gameDTO {
	t.Helper()
	return postWithToken(t, ts, "/api/games/"+id+"/rematch/decline", token, wantStatus)
}

// postDeclineInviteAs declines an invite as userID via X-Test-User-ID; the
// handler identifies the caller from the JWT context, not a player token.
func postDeclineInviteAs(t *testing.T, ts *httptest.Server, gameID string, seatIdx int, userID string, wantStatus int) gameDTO {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/games/"+gameID+"/seats/"+itoa(seatIdx)+"/invite/decline", nil)
	req.Header.Set("X-Test-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("decline invite: want=%d got=%d body=%s", wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusOK {
		return gameDTO{}
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func postWithToken(t *testing.T, ts *httptest.Server, path, token string, wantStatus int) gameDTO {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, nil)
	if token != "" {
		req.Header.Set("X-Player-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: want=%d got=%d body=%s", path, wantStatus, resp.StatusCode, b)
	}
	if wantStatus != http.StatusOK {
		return gameDTO{}
	}
	var g gameDTO
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

// finishGame lands a 6-alignment for Alice on the q-axis (instant win). Bob
// plays one row up so neither side sandwiches the other — no captures interfere.
func finishGame(t *testing.T, ts *httptest.Server, gameID, aliceTok, bobTok string) {
	t.Helper()
	for q := 0; q < 5; q++ {
		_ = postMove(t, ts, gameID, aliceTok, q, 0, http.StatusOK)
		_ = postMove(t, ts, gameID, bobTok, q, 1, http.StatusOK)
	}
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

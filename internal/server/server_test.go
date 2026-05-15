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
	return httptest.NewServer(New(log, store, nil, Config{}).Routes())
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
	// Public games can only come from matchmaking now — accept-as-public
	// on the create endpoint would let anyone open a permanent public room.
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
	// No JWT, no name → can't auto-join the creator anywhere.
	_ = createGameWithBody(t, ts, `{"players":2}`, http.StatusBadRequest)
}

func TestRematchOfferRequiresFinishedGame(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	// Game still waiting → /rematch/offer must 409 even with a valid token.
	_ = postRematchOffer(t, ts, g.ID, j1.Token, http.StatusConflict)
}

func TestRematchOfferUnanimous(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	// Private 2-player so we can join anonymously. The acceptance flow is
	// what we're testing; visibility carries through identically to before.
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)

	// Alice proposes — offer is created, no rematch game yet.
	d1 := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d1.RematchGameID != "" {
		t.Fatalf("rematch should not exist yet after one acceptance, got %q", d1.RematchGameID)
	}
	if d1.RematchOffer == nil || len(d1.RematchOffer.PendingSeats) != 1 || d1.RematchOffer.PendingSeats[0] != 1 {
		t.Fatalf("want Bob (seat 1) pending after Alice accepts, got %#v", d1.RematchOffer)
	}
	// Alice clicking again is idempotent: still pending Bob.
	d1b := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d1b.RematchGameID != "" {
		t.Fatalf("re-accept by Alice must not create rematch, got %q", d1b.RematchGameID)
	}
	// Bob accepts → rematch is created. The response is the original game's
	// DTO with RematchGameID populated; the new game has its own ID.
	d2 := postRematchOffer(t, ts, g.ID, j2.Token, http.StatusOK)
	if d2.RematchGameID == "" {
		t.Fatalf("rematch must be created once both seats have accepted")
	}
	if d2.RematchGameID == g.ID {
		t.Fatalf("rematch must spawn a *new* game, got the original ID back")
	}
	// Subsequent calls remain successful and resolve to the same rematch.
	d3 := postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	if d3.RematchGameID != d2.RematchGameID {
		t.Fatalf("rematch ID must stay stable on repeat accept; got %s then %s", d2.RematchGameID, d3.RematchGameID)
	}
	// Sanity: the rematch game itself exists, is waiting, and matches the
	// original's player count + visibility.
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

	// Alice offers, then Bob declines → offer cleared, no rematch.
	_ = postRematchOffer(t, ts, g.ID, j1.Token, http.StatusOK)
	d := postRematchDecline(t, ts, g.ID, j2.Token, http.StatusOK)
	if d.RematchOffer != nil {
		t.Fatalf("offer should be cleared after decline, got %#v", d.RematchOffer)
	}
	if d.RematchGameID != "" {
		t.Fatalf("decline must not create a rematch, got %q", d.RematchGameID)
	}
	// Declining when no offer is pending → 409.
	_ = postRematchDecline(t, ts, g.ID, j1.Token, http.StatusConflict)
}

// TestRematchOffer_RequiresToken locks in the auth posture: the propose /
// accept path runs through a player's seat token, not a JWT, so calling it
// without an X-Player-Token must 401.
func TestRematchOffer_RequiresToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	j1 := joinGame(t, ts, g.ID, "Alice", nil)
	j2 := joinGame(t, ts, g.ID, "Bob", nil)
	finishGame(t, ts, g.ID, j1.Token, j2.Token)
	_ = postRematchOffer(t, ts, g.ID, "", http.StatusUnauthorized)
}

// TestRematchOffer_ThreePlayers_NeedsAllThreeAccepts exercises the multi-N
// case: with three humans seated, the rematch only fires after every one of
// them has accepted. The pendingSeats projection shrinks at each step.
func TestRematchOffer_ThreePlayers_NeedsAllThreeAccepts(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 3)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	b := joinGame(t, ts, g.ID, "Bob", nil)
	c := joinGame(t, ts, g.ID, "Carol", nil)
	// Quickest finish for a 3P game: one player resigns. Game flips to
	// finished regardless of how many humans are left.
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

// TestRematchOffer_BotIsPreAccepted documents the bot shortcut: with a bot
// at the table, only the human needs to click "Revanche" — the bot's
// acceptance is implicit since there's nobody to ask. This keeps the UX
// instant for the "play vs bot" loop.
func TestRematchOffer_BotIsPreAccepted(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	a := joinGame(t, ts, g.ID, "Alice", nil)
	// Bot fills seat 1; AllSeated flips status to playing. The bot's
	// scheduled goroutine may attempt a move before Alice's resign acquires
	// the lock, but either ordering ends with the bot Occupied + IsBot, so
	// the offer's pre-acceptance check still picks it up.
	_ = postAddBot(t, ts, g.ID, 1, http.StatusOK)
	postResign(t, ts, g.ID, a.Token, http.StatusOK)

	d := postRematchOffer(t, ts, g.ID, a.Token, http.StatusOK)
	if d.RematchGameID == "" {
		t.Fatalf("rematch must be created on Alice's single accept (bot pre-accepted), got %#v", d)
	}
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
	// /api/games/{id} carries the same shape (the WS state event reuses
	// toGameDTO), so verify it survives the round-trip.
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
	// Bob tries to decline Alice's invitation → 403.
	_ = postDeclineInviteAs(t, ts, g.ID, 1, "bob-uuid", http.StatusForbidden)
}

func TestDeclineSeatInvite_RejectsEmptySeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	// No invitation on seat 1 → ErrSeatNotInvited → 409.
	_ = postDeclineInviteAs(t, ts, g.ID, 1, "alice-uuid", http.StatusConflict)
}

// TestInviteSeat_PushesInviteReceivedOverLobbyWS pins the cross-page
// notification path: the host's POST /seats/{idx}/invite must wake up
// the invitee's lobby WS (the persistent per-user connection that
// AuthProvider keeps open) with an `invite_received` event so the
// global toast can render even when the invitee is not on the game
// page yet.
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

	// Fire the invite from another goroutine; we'll block on Read
	// until the event lands.
	go func() {
		_ = postInviteSeat(t, ts, g.ID, 1, aliceID, "Alice", http.StatusOK)
	}()

	ev := readEvent(t, ctx, conn)
	if ev.Type != "invite_received" {
		t.Fatalf("want invite_received, got %s", ev.Type)
	}
	// Payload arrives as json.RawMessage on the wire — decode and
	// verify the routable fields. We don't assert FromName because
	// the inviter goes through anonymous in the hermetic test.
	raw, ok := ev.Payload.(map[string]any)
	if !ok {
		// readEvent decodes Payload as interface{}; a JSON object lands
		// as map[string]any when unmarshalled into Event.
		t.Fatalf("want object payload, got %T", ev.Payload)
	}
	if got, _ := raw["gameId"].(string); got != g.ID {
		t.Fatalf("want gameId=%s, got %v", g.ID, raw["gameId"])
	}
	if got, _ := raw["seatIndex"].(float64); int(got) != 1 {
		t.Fatalf("want seatIndex=1, got %v", raw["seatIndex"])
	}
}

// TestCancelSeatInvite_PushesInviteCancelledOverLobbyWS covers the
// inverse: a host clicking "× Annuler" while the invitee's toast is
// open must dismiss it cleanly via an `invite_cancelled` push.
func TestCancelSeatInvite_PushesInviteCancelledOverLobbyWS(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const aliceID = "alice-uuid"
	g := createGame(t, ts, 2)
	// Reserve the seat before opening the WS so the invitee subscribes
	// in time for the cancel notification.
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
	// No X-Test-User-ID → JWT middleware leaves the context unauthenticated,
	// and the handler 401s before reaching the store.
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
	// Alice is on seat 0 as a real player, not as an invite — cancel-invite
	// must refuse rather than evict her.
	postCancelSeatInviteRaw(t, ts, g.ID, 0, http.StatusConflict)
}

func TestJoin_InvitedUserGetsReservedSeat(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 2)
	// Reserve seat 1 for alice-uuid.
	_ = postInviteSeat(t, ts, g.ID, 1, "alice-uuid", "Alice", http.StatusOK)
	// Then alice joins (auto-pick, no seat=). The reserved seat takes
	// priority over the empty seat 0.
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
	// Bob explicitly asks for seat 1 → blocked (reserved for Alice).
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
	// Add then remove — seat 1 ends back at empty.
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
	// Alice is in seat 0; removeBot must refuse — it's not a bot seat.
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

func TestStartTrimsToOccupiedSeats(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6) // private, 6 seats, all empty
	a := joinGame(t, ts, g.ID, "Alice", nil)
	b := joinGame(t, ts, g.ID, "Bob", nil)
	// Private games must NOT auto-start when AllSeated — but here only 2/6
	// are seated so the assertion is doubly safe.
	if a.Game.Status != StatusWaiting || b.Game.Status != StatusWaiting {
		t.Fatalf("game must stay waiting before Start (status=%s)", b.Game.Status)
	}
	out := postStart(t, ts, g.ID, a.Token, http.StatusOK)
	if out.Status != StatusPlaying {
		t.Fatalf("want playing after Start, got %s", out.Status)
	}
	// Trim: only the previously-occupied 2 seats survive — empty ones are
	// gone entirely from the wire view.
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

// TestStartRecomputesThresholdsForOccupiedCount: a 6-slot private game
// started with only 3 occupied seats must play under the 3-player rulebook
// (Align4ToWin=6, Align5ToWin=3, CapturePairsWin=10), not the 6-player one.
// The thresholds are picked at Start, not at Create.
func TestStartRecomputesThresholdsForOccupiedCount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6) // 6-slot private; would default to 6-player rules
	// During waiting with 0 occupied seats, the preview falls back to the
	// 2-player table (the minimum) — this is intentional: the host's
	// thresholds preview should never claim 6-player rules with 0 seated.
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

// TestWaitingPreviewsThresholdsByOccupiedCount: the thresholds surfaced on
// the wire while the room is still waiting must reflect the *current*
// occupancy, not the slot count. This is what the lobby UI binds to so the
// host sees the rules they're about to commit to.
func TestWaitingPreviewsThresholdsByOccupiedCount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	g := createGame(t, ts, 6) // 6-slot private
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
	// Alice on seat 0 (the host slot — only that one may call Start),
	// Bob on seat 4 (C5) so the pair is non-contiguous and the trim
	// has actual gaps to close.
	zero := 0
	four := 4
	a := joinGame(t, ts, g.ID, "Alice", &zero)
	b := joinGame(t, ts, g.ID, "Bob", &four)
	out := postStart(t, ts, g.ID, a.Token, http.StatusOK)
	// After trim, Alice keeps C1 and Bob keeps C5 — the engine doesn't
	// re-colour them, just re-orders. Names follow the colour.
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

	// A guest's token must NOT start the game even though they hold a
	// valid seat token — otherwise anyone who joined could race the
	// host on Start before the lobby is fully arranged.
	postStartRaw(t, ts, g.ID, guest.Token, http.StatusForbidden)
	// The host can.
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
	// A public game spawned via matchmake by Alice. The URL is technically
	// reachable by anyone who knows the ID — anonymous join must be
	// rejected so this surface can't bypass the matchmaking-only contract.
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
	// createGame defaults to private — anonymous join must keep working
	// so URL-sharing for casual play isn't broken.
	g := createGame(t, ts, 2)
	_ = joinGame(t, ts, g.ID, "Anon", nil)
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

// TestGameRatings_NoopFallsBackToUnrated guards the API contract of
// the rated-game-end modal: in hermetic mode (no DB) the endpoint
// always reports rated:false with an empty seats list, so the client
// can render a generic end-of-game card instead of crashing.
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

// TestGetMe_AutoFillsDisplayName guards the leaderboard-visibility fix.
// A user that authenticates without having gone through the explicit
// "set display name" form must still get a usable displayName from
// /api/auth/me — derived from their email when nothing else is on
// record. Without the fix, the response was {displayName: ""} and the
// matching profile row was never created.
func TestGetMe_AutoFillsDisplayName(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/auth/me", nil)
	// X-Test-User-ID is the hermetic auth back door: the JWT
	// middleware turns it into AuthUser{ID, Email: id+"@test.local"}
	// when no JWT verifier is configured.
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
	// Email is <id>@test.local, local-part = the UUID, so the fallback
	// derives that. Anything non-empty proves we hit the EnsureProfile
	// path; the noop repo doesn't persist, so we're really asserting
	// the handler builds and returns a synthetic Profile when none
	// exists in the store.
	if dto.DisplayName == "" {
		t.Fatalf("displayName must be auto-filled even when no profile row exists; got %+v", dto)
	}
}

// ---- helpers ----

// createGame produces a private game with every seat still empty, matching
// the old test convention. The HTTP endpoint atomically auto-joins the
// caller now, so we do create-then-leave under the hood so the legacy
// tests can treat the returned game as "fresh, nobody seated yet".
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
	// Vacate the seed seat so the caller can treat the game as empty.
	leaveResp, err := postLeave(t, ts, j.Game.ID, j.Token)
	if err != nil {
		t.Fatal(err)
	}
	leaveResp.Body.Close()
	// Re-fetch the now-empty game state.
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

// joinGameAs is the authenticated variant of joinGame — it sends the
// hermetic X-Test-User-ID so the server treats the join as coming
// from a logged-in user with the given user id. Needed for the
// reserved-seat tests: only an authed caller can land on a seat that
// was invited for them.
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

func postRematchOffer(t *testing.T, ts *httptest.Server, id, token string, wantStatus int) gameDTO {
	t.Helper()
	return postWithToken(t, ts, "/api/games/"+id+"/rematch/offer", token, wantStatus)
}

func postRematchDecline(t *testing.T, ts *httptest.Server, id, token string, wantStatus int) gameDTO {
	t.Helper()
	return postWithToken(t, ts, "/api/games/"+id+"/rematch/decline", token, wantStatus)
}

// postDeclineInviteAs sends the invitee-side decline call with the
// hermetic X-Test-User-ID header — the handler uses userFromContext
// (not a player token) to identify the caller, so this mirrors the
// joinGameAs auth path.
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

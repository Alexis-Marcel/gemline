package server

// Rematch flow — the chess.com-style "propose, accept, spawn the
// follow-up game" state machine. Pulled out of store.go for the same
// reason as draw.go: it's a self-contained domain concept with its own
// types (RematchSeat) and helpers (neededRematchSeats, etc.).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alexis/gemline/internal/game"
)

func neededRematchSeats(rec *GameRecord) []int {
	out := make([]int, 0, len(rec.Seats))
	for i, st := range rec.Seats {
		if st.Occupied && !st.IsBot {
			out = append(out, i)
		}
	}
	return out
}

// newRematchOffer builds a fresh offer with bot seats pre-marked accepted.
// Caller must hold rec's lock.
func newRematchOffer(rec *GameRecord) *RematchOffer {
	accepted := make(map[int]bool)
	for i, st := range rec.Seats {
		if st.Occupied && st.IsBot {
			accepted[i] = true
		}
	}
	return &RematchOffer{AcceptedSeats: accepted, CreatedAt: time.Now()}
}

// rematchOfferComplete reports whether every needed (human-occupied) seat has
// accepted the current offer. Caller must hold rec's lock.
func rematchOfferComplete(rec *GameRecord) bool {
	if rec.RematchOffer == nil {
		return false
	}
	for _, idx := range neededRematchSeats(rec) {
		if !rec.RematchOffer.AcceptedSeats[idx] {
			return false
		}
	}
	return true
}

// OfferRematch records that the seat behind `token` accepts a rematch on a
// finished game. If no offer is active yet, this call creates one (with bot
// seats pre-accepted) and marks the caller as the first acceptor. Subsequent
// calls add their seat to the acceptance set. When every needed human seat
// has accepted, the new game is created via Rematch and rec.RematchGameID
// gets set — clients see that on the next state event and navigate over.
//
// When the accept call triggers creation, the returned []RematchSeat
// carries fresh tokens for every authed human pre-seated in the new game.
// The caller publishes them via the lobby so each player picks up their
// rematch credentials without an explicit re-join. Nil on every other
// path (first/intermediate acceptance, race-loss, already-created offer).
//
// Idempotent: a player who has already accepted gets a no-op success.
func (s *Store) OfferRematch(ctx context.Context, gameID, token string) (*GameRecord, []RematchSeat, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, nil, err
	}

	rec.Lock()
	if rec.Status != StatusFinished {
		rec.Unlock()
		return rec, nil, ErrNotFinished
	}
	if rec.RematchGameID != "" {
		// Already created — caller can just navigate to it.
		rec.Unlock()
		return rec, nil, nil
	}
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, nil, ErrBadToken
	}
	if rec.RematchOffer == nil {
		rec.RematchOffer = newRematchOffer(rec)
	}
	rec.RematchOffer.AcceptedSeats[seat.Index] = true
	complete := rematchOfferComplete(rec)
	// Snapshot the offer for persistence while we still hold the lock.
	// Multi-pod safety: without this write the second player's
	// acceptance on a different pod would land on a cache-miss reload
	// of the game and create a fresh offer with only their seat.
	offerBlob, _ := json.Marshal(rec.RematchOffer)
	rec.Unlock()

	if err := s.repo.SaveRematchOffer(ctx, gameID, offerBlob); err != nil {
		// Best-effort: log via the listener path is overkill, and the
		// caller will get a fresh state via WS anyway. Surface up so
		// the HTTP response is consistent.
		return rec, nil, err
	}

	if !complete {
		return rec, nil, nil
	}
	// Unanimous acceptance — create the rematch game. Rematch() does its own
	// locking + race handling against parallel callers, so we drop rec.Lock
	// before calling. On success the link is committed both in the repo and on
	// rec.RematchGameID. Clear the now-moot offer state in memory + DB.
	_, authedSeats, err := s.Rematch(ctx, gameID)
	if err != nil {
		return rec, nil, err
	}
	rec.Lock()
	rec.RematchOffer = nil
	rec.Unlock()
	_ = s.repo.SaveRematchOffer(ctx, gameID, nil)
	return rec, authedSeats, nil
}

// DeclineRematch clears any pending rematch offer on a finished game. Either
// the proposer (withdrawing) or any invited acceptor (refusing) may call it —
// we don't distinguish them, the outcome is the same: the offer disappears
// and everyone returns to the "propose rematch" state.
func (s *Store) DeclineRematch(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if rec.Status != StatusFinished {
		rec.Unlock()
		return rec, ErrNotFinished
	}
	if _, ok := rec.SeatByToken(token); !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.RematchOffer == nil {
		rec.Unlock()
		return rec, ErrNoRematchOffer
	}
	rec.RematchOffer = nil
	rec.Unlock()
	// Clear in DB so other pods don't resurrect the offer on their
	// next reload.
	_ = s.repo.SaveRematchOffer(ctx, gameID, nil)
	return rec, nil
}

// RematchSeat carries the per-seat credentials issued to an authenticated
// human pre-seated in a freshly-created rematch game. The server pushes
// these via lobby `rematch_ready` events so each pre-seated user gets a
// usable token without an extra join call; the plaintext Token never

type RematchSeat struct {
	SeatIndex int
	UserID    string
	Name      string
	Token     string
}

// Rematch creates a fresh game with the same player count, config and
// visibility as `originalID`, pre-seats the bots + authenticated humans
// who were in the original, and links the two via rematch_game_id. The
// operation is idempotent: a second caller after the link is set is sent
// to the same rematch game. The original game must be finished.
//
// The returned RematchSeat slice carries per-user tokens for any
// authenticated humans we pre-seated; the caller is responsible for
// pushing them via the lobby so the clients can pick up the new game
// without re-joining. On a race-loss, the slice is empty — the pod that
// won has already published its own tokens.
//
// Anonymous players are deliberately not pre-seated: we have no way to
// deliver them a fresh token, so they re-join the rematch through the
// normal POST /api/games/{id}/join path (auto-join handles it client-side).
func (s *Store) Rematch(ctx context.Context, originalID string) (*GameRecord, []RematchSeat, error) {
	orig, ok, err := s.Get(ctx, originalID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ErrGameNotFound
	}

	orig.Lock()
	if orig.Status != StatusFinished {
		orig.Unlock()
		return nil, nil, ErrNotFinished
	}
	if linked := orig.RematchGameID; linked != "" {
		orig.Unlock()
		// Another caller already created the rematch — fetch and return it.
		rec, ok, err := s.Get(ctx, linked)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			// The link points to a game that no longer exists (rare —
			// ON DELETE SET NULL handles the FK at the DB layer, but the
			// in-memory copy could still hold a stale ID). Treat as
			// "no rematch yet" and create a new one.
		} else {
			return rec, nil, nil
		}
	}
	numPlayers := len(orig.Seats)
	vis := orig.Visibility
	origCfg := orig.State.Config
	// Snapshot the seat layout under the lock so we can build the rematch's
	// seats without holding the original game's lock through the DB write.
	origSeats := append([]Seat(nil), orig.Seats...)
	orig.Unlock()

	// Build the new game using the same shape as Create. We don't call Create
	// directly because we want to atomically write the rematch_game_id link on
	// the original game alongside the new game's row. Carry the original
	// game's clock settings through ConfigFor so a rematch inherits the prior
	// time control (rules are at the rematch's player count, clock is the
	// same as last time).
	//
	// Mirror the original seating: bots come back at the same seats (we own
	// their tokens, no delivery needed), authed humans come back with fresh
	// tokens that get pushed via the lobby, and anon humans are left empty
	// (no way to hand them a token). If every seat ends up occupied, the
	// game starts in `playing` straight away — no SearchingForOpponent flash
	// on the public 1v1 rematch path.
	colors := make([]game.Color, numPlayers)
	seats := make([]Seat, numPlayers)
	var authedSeats []RematchSeat
	allOccupied := true
	for i := 0; i < numPlayers; i++ {
		colors[i] = game.Color(i + 1)
		seats[i] = Seat{Index: i, Color: colors[i]}
		prior := origSeats[i]
		switch {
		case !prior.Occupied:
			allOccupied = false
		case prior.IsBot:
			tok := newToken()
			seats[i].Name = botName(i)
			seats[i].TokenHash = hashToken(tok)
			seats[i].Occupied = true
			seats[i].IsBot = true
		case prior.UserID != "":
			tok := newToken()
			seats[i].Name = prior.Name
			seats[i].UserID = prior.UserID
			seats[i].TokenHash = hashToken(tok)
			seats[i].Occupied = true
			authedSeats = append(authedSeats, RematchSeat{
				SeatIndex: i,
				UserID:    prior.UserID,
				Name:      prior.Name,
				Token:     tok,
			})
		default:
			// Anonymous human: no UserID, no way to deliver a token. They
			// fall back to the join path when their client lands on the
			// rematch URL.
			allOccupied = false
		}
	}

	status := StatusWaiting
	if allOccupied {
		status = StatusPlaying
	}
	rec := &GameRecord{
		ID:          newID(),
		State:       game.NewGame(colors, game.ConfigFor(numPlayers, origCfg)),
		Seats:       seats,
		Status:      status,
		Visibility:  vis,
		CreatedAt:   time.Now(),
		DrawOfferBy: -1,
	}
	if status == StatusPlaying {
		rec.State.StartClock(time.Now())
	}
	if err := s.repo.SaveNewGame(ctx, rec); err != nil {
		return nil, nil, err
	}

	// Link original → new. If two goroutines raced past the early-out above,
	// the repo's SetRematchLink resolves the race: the loser observes that the
	// link is already set and returns the winner's game ID.
	winnerID, err := s.repo.SetRematchLink(ctx, originalID, rec.ID)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	if existing, found := s.games[winnerID]; found && winnerID != rec.ID {
		// Lost the race — discard the freshly-built record and return the
		// winner's. The orphaned `rec` row remains in the DB but is unlinked;
		// it'll age out via normal cleanup paths. The winner's pod has
		// already published its own seat tokens, so we suppress ours.
		s.mu.Unlock()
		orig.Lock()
		orig.RematchGameID = winnerID
		orig.Unlock()
		return existing, nil, nil
	}
	if winnerID == rec.ID {
		s.games[rec.ID] = rec
	}
	s.mu.Unlock()

	orig.Lock()
	orig.RematchGameID = winnerID
	orig.Unlock()

	if winnerID != rec.ID {
		// Race lost but the cache didn't have the winner — fetch through Get.
		winner, _, err := s.Get(ctx, winnerID)
		if err != nil {
			return nil, nil, err
		}
		return winner, nil, nil
	}
	if status == StatusPlaying {
		// Mirror the seat-claiming paths (AddBot / Join): start the timeout
		// timer, then nudge the active seat in case it's a bot.
		s.armClock(rec)
		go s.maybeScheduleBot(rec)
	}
	return rec, authedSeats, nil
}

// getOrNotFound is a small convenience wrapper over Get that folds the
// "not found" boolean into ErrGameNotFound. Every mutation handler in
// this file used to inline the same three-line dance; this returns
// a single value pair that's hard to misuse — you can't forget the
// not-found check.

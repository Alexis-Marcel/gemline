package server

// Rematch flow: propose / accept, then spawn the follow-up game.

import (
	"context"
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

// OfferRematch records the token's seat accepting a rematch, creating the offer
// (bots pre-accepted) on the first call. When every human seat has accepted it
// creates the new game and sets rec.RematchGameID. On that final call it returns
// fresh seat tokens for the pre-seated authed humans (nil otherwise) for the
// caller to push via the lobby. Idempotent for a seat that already accepted.
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
	// Snapshot the bot seat indices so the repo can pre-accept them when it
	// initialises a fresh offer. Done under the lock so we read a consistent
	// seat layout.
	var botSeats []int
	for i, st := range rec.Seats {
		if st.Occupied && st.IsBot {
			botSeats = append(botSeats, i)
		}
	}
	rec.Unlock()

	// Atomic merge at the DB layer. With a Postgres repo this runs as a
	// SELECT ... FOR UPDATE + read-modify-write + UPDATE in one transaction,
	// so a second pod whose cache hadn't yet been invalidated by the first
	// pod's NOTIFY can't blindly overwrite the first acceptance with a
	// fresh single-seat offer — the cause of the "accept does nothing"
	// rematch bug under load on a multi-pod deploy.
	merged, err := s.repo.MergeRematchAcceptance(ctx, gameID, seat.Index, botSeats)
	if err != nil {
		return rec, nil, err
	}
	// The noop repo (no DATABASE_URL — single-process mode) returns nil:
	// there's no cross-pod race to guard against, so we fall back to the
	// in-memory merge.
	if merged == nil {
		rec.Lock()
		if rec.RematchOffer == nil {
			rec.RematchOffer = newRematchOffer(rec)
		}
		rec.RematchOffer.AcceptedSeats[seat.Index] = true
		merged = rec.RematchOffer
		rec.Unlock()
	}

	// Sync the in-memory cache from the authoritative merged offer and
	// decide whether we just completed it.
	rec.Lock()
	rec.RematchOffer = merged
	complete := rematchOfferComplete(rec)
	rec.Unlock()

	if !complete {
		return rec, nil, nil
	}
	// Unanimous — create the rematch game. Rematch() does its own locking and
	// race handling, so call it without rec.Lock, then clear the moot offer.
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

// DeclineRematch clears a pending offer on a finished game — withdraw or refuse,
// same outcome.
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
	// Clear in DB so other pods don't resurrect the offer on reload.
	_ = s.repo.SaveRematchOffer(ctx, gameID, nil)
	return rec, nil
}

// RematchSeat carries the fresh credentials for an authed human pre-seated in a
// new rematch game, pushed via lobby `rematch_ready` events.
type RematchSeat struct {
	SeatIndex int
	UserID    string
	Name      string
	Token     string
}

// Rematch creates a fresh game mirroring originalID (player count, config,
// visibility), pre-seats the original's bots + authed humans, and links the two
// via rematch_game_id. Idempotent: a later caller is sent to the same game.
// Returns fresh tokens for pre-seated authed humans (empty on a race-loss).
// Anonymous players aren't pre-seated — they re-join via the normal join path.
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

	// Build the new game inline (not via Create) so we can write the
	// rematch_game_id link atomically. Mirror the original seating: bots and
	// authed humans return (authed humans get fresh tokens pushed via the
	// lobby), anon humans are left empty. All-occupied starts in `playing`.
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

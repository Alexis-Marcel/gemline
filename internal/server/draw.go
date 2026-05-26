package server

// Draw-offer state machine for in-flight games. Lives in its own file
// so the OfferDraw / AcceptDraw / DeclineDraw flow + the publish helper
// stay grouped — they share the same persistence + NOTIFY ordering
// invariants (persist before NOTIFY so cross-pod cache reloads see the
// new value).

import (
	"context"
)

func (s *Store) publishDrawOffer(ctx context.Context, gameID string, offerBy int) error {
	if err := s.repo.SaveDrawOffer(ctx, gameID, offerBy); err != nil {
		return err
	}
	if s.onDrawOffer != nil {
		s.onDrawOffer(gameID, offerBy)
	}
	return nil
}

// Get fetches a game, falling back to the repo if it isn't cached. Returns
// (nil, false, nil) if no such game exists anywhere.

func (s *Store) OfferDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	if len(rec.Seats) != 2 {
		rec.Unlock()
		return rec, ErrDrawUnsupported
	}
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.DrawOfferBy >= 0 {
		if rec.DrawOfferBy == seat.Index {
			// Re-offering by the same player is a no-op — return success so
			// the UI's optimistic state matches the server.
			rec.Unlock()
			return rec, nil
		}
		// The opponent is already offering; the right action is "accept",
		// not "offer". Surface this distinction to the caller.
		rec.Unlock()
		return rec, ErrDrawAlreadyOffered
	}
	rec.DrawOfferBy = seat.Index
	offeredBy := seat.Index
	rec.Unlock()

	// publishDrawOffer enforces the persist-then-notify order so a
	// cross-pod accept call landing on a different pod always reloads
	// the new draw_offer_by from the DB.
	if err := s.publishDrawOffer(ctx, gameID, offeredBy); err != nil {
		return rec, err
	}
	return rec, nil
}

// AcceptDraw ends the game in a draw if the *opponent* has an offer pending.
// Self-acceptance is rejected. 2-player only.
func (s *Store) AcceptDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	seat, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.DrawOfferBy < 0 {
		rec.Unlock()
		return rec, ErrDrawNotOffered
	}
	if rec.DrawOfferBy == seat.Index {
		rec.Unlock()
		return rec, ErrCannotAcceptOwnDrawOffer
	}
	rec.State.AgreeDraw()
	rec.Status = StatusFinished
	rec.DrawOfferBy = -1
	winner := rec.State.Winner
	winKind := rec.State.WinKind
	rec.Unlock()

	s.gameEnded(gameID)
	if err := s.repo.UpdateOutcome(ctx, gameID, StatusFinished, winner, winKind); err != nil {
		noteSwallowedErr("accept_draw_outcome_persist", err)
	}
	// Best-effort: surface a clear draw_offer_by to other pods so the
	// finished DTO doesn't keep a phantom offerer. Persist failure here
	// doesn't roll the engine back — in-memory truth still wins.
	if err := s.publishDrawOffer(ctx, gameID, -1); err != nil {
		noteSwallowedErr("accept_draw_clear", err)
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
	return rec, nil
}

// DeclineDraw clears a pending draw offer. Either the opponent (refusing the
// offer) or the offering player (withdrawing it) may call this — we don't
// distinguish them at the wire level.
func (s *Store) DeclineDraw(ctx context.Context, gameID, token string) (*GameRecord, error) {
	rec, err := s.getOrNotFound(ctx, gameID)
	if err != nil {
		return nil, err
	}

	rec.Lock()
	_, ok := rec.SeatByToken(token)
	if !ok {
		rec.Unlock()
		return rec, ErrBadToken
	}
	if rec.Status != StatusPlaying {
		rec.Unlock()
		return rec, ErrNotPlaying
	}
	if rec.DrawOfferBy < 0 {
		rec.Unlock()
		return rec, ErrDrawNotOffered
	}
	rec.DrawOfferBy = -1
	rec.Unlock()

	if err := s.publishDrawOffer(ctx, gameID, -1); err != nil {
		return rec, err
	}
	return rec, nil
}

// maybeScheduleBot inspects the current state of `rec` and, if it is a bot's
// turn, kicks off a goroutine that will play one move after `s.botDelay`.
// Safe to call whether or not the caller holds rec.Lock — the goroutine does
// its own locking.

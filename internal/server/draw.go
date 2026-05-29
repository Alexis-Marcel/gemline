package server

// Draw-offer state machine for 2-player games. Persist before NOTIFY so a
// cross-pod accept reloads the new draw_offer_by from the DB.

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
			// Same player re-offering: no-op success so optimistic UI matches.
			rec.Unlock()
			return rec, nil
		}
		// Opponent already offering — the right action is accept, not offer.
		rec.Unlock()
		return rec, ErrDrawAlreadyOffered
	}
	rec.DrawOfferBy = seat.Index
	offeredBy := seat.Index
	rec.Unlock()

	if err := s.publishDrawOffer(ctx, gameID, offeredBy); err != nil {
		return rec, err
	}
	return rec, nil
}

// AcceptDraw ends the game in a draw if the opponent has an offer pending.
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
	if err := s.publishDrawOffer(ctx, gameID, -1); err != nil {
		noteSwallowedErr("accept_draw_clear", err)
	}
	s.maybeApplyRating(rec)
	if s.onState != nil {
		s.onState(gameID)
	}
	return rec, nil
}

// DeclineDraw clears a pending offer. Either side may call it (refuse or
// withdraw); the wire doesn't distinguish them.
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

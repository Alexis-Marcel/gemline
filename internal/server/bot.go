package server

// Bot move loop. botName names a bot seat ("Bot Rouge" etc.),
// maybeScheduleBot kicks the active bot after the configured delay,
// playBotTurnIfNeeded is the worker that consults the engine and
// applies a move under the record lock.

import (
	"context"
	"time"

	"github.com/alexis/gemline/internal/game"
)

func botName(seatIndex int) string {
	switch seatIndex {
	case 0:
		return "Bot Rouge"
	case 1:
		return "Bot Bleu"
	case 2:
		return "Bot Vert"
	case 3:
		return "Bot Jaune"
	case 4:
		return "Bot Violet"
	default:
		return "Bot"
	}
}

// LobbyEntry is a slimmed-down view of a public waiting game, returned by the
// lobby endpoint. We deliberately don't include seat tokens or chat history —

func (s *Store) maybeScheduleBot(rec *GameRecord) {
	if rec == nil {
		return
	}
	go func() {
		if s.botDelay > 0 {
			time.Sleep(s.botDelay)
		}
		s.playBotTurnIfNeeded(rec)
	}()
}

// playBotTurnIfNeeded plays exactly one move on behalf of the active bot, if
// the game is still in progress and the active player is a bot. After the
// move it recursively schedules the next turn — which means bot-vs-bot games
// progress automatically without external prodding.
func (s *Store) playBotTurnIfNeeded(rec *GameRecord) {
	rec.Lock()
	if rec.Status != StatusPlaying || rec.State.IsOver() {
		rec.Unlock()
		return
	}
	seatIdx := rec.State.Turn
	if seatIdx < 0 || seatIdx >= len(rec.Seats) {
		rec.Unlock()
		return
	}
	seat := &rec.Seats[seatIdx]
	if !seat.IsBot {
		rec.Unlock()
		return
	}
	color := seat.Color
	pos, ok := s.botEngine.BestMove(rec.State, color)
	if !ok {
		rec.Unlock()
		return
	}

	ordinal := len(rec.State.History)
	move := game.Move{Player: color, Pos: pos}
	res, err := rec.State.ApplyMove(move, time.Now())
	if err != nil {
		// BestMove returned an illegal position (shouldn't happen — we only
		// consider Empty in-bounds cells), or the engine rejected for some
		// other reason. Skip rather than crash: a human player can still
		// resign or wait for a flag.
		rec.Unlock()
		return
	}
	if rec.State.IsOver() {
		rec.Status = StatusFinished
	}
	hadDrawOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1 // any draw offer is auto-rescinded by a move

	winner := rec.State.Winner
	winKind := rec.State.WinKind
	status := rec.Status
	gameID := rec.ID
	s.armClock(rec)
	rec.Unlock()

	// Persist with the same shape PlayMove uses — through AppendMove for
	// the move log + status update. We don't have a request context here
	// since this is server-driven; Background is fine.
	if err := s.repo.AppendMove(context.Background(), gameID, ordinal, move, winner, winKind, status); err != nil {
		// In-memory truth wins (same policy as the human path).
		noteSwallowedErr("bot_move_persist", err)
	}
	if hadDrawOffer {
		if err := s.repo.SaveDrawOffer(context.Background(), gameID, -1); err != nil {
			noteSwallowedErr("bot_move_draw_clear", err)
		}
	}

	if s.onMove != nil {
		s.onMove(gameID, res)
	}
	// Continue the chain — if the next active player is also a bot, this
	// schedules another move; if it's a human's turn, this is a no-op.
	s.maybeScheduleBot(rec)
}

// maybeApplyRating is called every time a game's Status flips to Finished.
// It runs the Elo update asynchronously: eligibility is checked under the
// rec.Lock, then the persistence happens in its own goroutine because
// ApplyRatedGame opens a DB transaction we don't want serialised behind the
// game lock. Idempotency lives in the repo (UPDATE … WHERE rated_at IS NULL),
// so calling this twice from different code paths is safe.
//
// Eligible games: visibility=public AND every seat is an authenticated
// human (no bots, no anonymous). 2-player games update the 1v1 rating
// pool; 3+ player games update the multi pool via the moyenne-des-
// adversaires zero-sum extension (see elo.UpdateMulti). Games that end
// without a winner (multi timeout — Winner == Empty) are skipped: there's
// nothing to credit.

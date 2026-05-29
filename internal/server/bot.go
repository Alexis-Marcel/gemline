package server

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

// playBotTurnIfNeeded plays one move for the active bot if the game is live and
// it's a bot's turn, then schedules the next — so bot-vs-bot games self-advance.
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
		// Illegal move from the engine: skip rather than crash so humans can
		// still resign or flag.
		rec.Unlock()
		return
	}
	if rec.State.IsOver() {
		rec.Status = StatusFinished
	}
	hadDrawOffer := rec.DrawOfferBy >= 0
	rec.DrawOfferBy = -1 // a move auto-rescinds any draw offer

	winner := rec.State.Winner
	winKind := rec.State.WinKind
	status := rec.Status
	gameID := rec.ID
	s.armClock(rec)
	rec.Unlock()

	if err := s.repo.AppendMove(context.Background(), gameID, ordinal, move, winner, winKind, status); err != nil {
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
	s.maybeScheduleBot(rec)
}

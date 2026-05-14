package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alexis/gemline/internal/elo"
	"github.com/coder/websocket"
)

// Lobby plumbing. The user-facing flow is:
//
//   1. Authenticated client POSTs /api/matchmake/enqueue → Store.Enqueue
//      inserts (or refreshes) a row in matchmake_queue.
//   2. Client opens GET /ws/lobby. The handler subscribes them to the
//      lobby Hub keyed by their userID; the connection stays open
//      until they're matched or they navigate away.
//   3. The matcher (running on every pod) periodically locks queue
//      rows, pairs them, creates games + seats, deletes the rows.
//      For every matched seat it publishes one lobby notification.
//   4. The backplane delivers the notification to every pod. The pod
//      that hosts the user's lobby WS fans out a "match_found" event
//      with the game ID + the seat token; the client redirects.
//   5. The lobby WS close handler cancels the user's ticket (idempotent
//      after a successful match has already removed it).

// LobbyEnqueueRequest is the POST /api/matchmake/enqueue body.
// Players is 2 for 1v1, 3-6 for multi. Mode is derived server-side
// from players (1v1 vs multi) so a client can't trick its ticket into
// the wrong rating bucket.
type LobbyEnqueueRequest struct {
	Players int `json:"players"`
}

// LobbyMatchPayload is what the lobby WS emits when the matcher pairs
// the user. The client uses gameId for the redirect, token to
// authenticate the seat without a follow-up join call, and name for
// "You're playing as <name>" before the full game state loads.
type LobbyMatchPayload struct {
	GameID    string `json:"gameId"`
	Token     string `json:"token"`
	SeatIndex int    `json:"seatIndex"`
	Name      string `json:"name"`
}

// lobbyEnvelope is the JSON shape sent through ChannelLobby. UserID
// is the routing key the LobbyHub keys on; payload is forwarded as
// the WS event payload.
type lobbyEnvelope struct {
	UserID  string            `json:"userId"`
	Payload LobbyMatchPayload `json:"payload"`
}

// fanMatched is the matcher's onMatched callback. For every matched
// seat it emits one NOTIFY on the lobby channel; pods that host the
// affected user's lobby WS pick it up via handleLobbyNotif and
// deliver. Failures are logged but do not retry — the user is in the
// game regardless, and on next lobby WS open they'll see the dangling
// state cleaned up (a refresh / page nav will hit the new game).
func (s *Server) fanMatched(seats []MatchedSeat) {
	if s.backplane == nil {
		// No backplane in single-process / no-DB mode. Deliver locally
		// so a dev with one server + one client can still test the flow.
		for _, seat := range seats {
			s.lobby.Deliver(seat.UserID, Event{
				Type:    "match_found",
				Payload: LobbyMatchPayload{GameID: seat.GameID, Token: seat.Token, SeatIndex: seat.SeatIndex, Name: seat.Name},
			})
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	for _, seat := range seats {
		env, err := json.Marshal(lobbyEnvelope{
			UserID:  seat.UserID,
			Payload: LobbyMatchPayload{GameID: seat.GameID, Token: seat.Token, SeatIndex: seat.SeatIndex, Name: seat.Name},
		})
		if err != nil {
			s.log.Error("lobby fan: marshal", "err", err)
			continue
		}
		if err := s.backplane.Publish(ctx, ChannelLobby, env); err != nil {
			s.log.Error("lobby fan: notify", "user", seat.UserID, "err", err)
		}
	}
}

// handleLobbyNotif is registered as the ChannelLobby handler in
// Server.New. Each pod receives every notification; routing by userID
// happens locally — pods without a sub for the targeted user no-op.
func (s *Server) handleLobbyNotif(payload []byte) {
	var env lobbyEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		s.log.Warn("lobby notif: bad envelope", "err", err)
		return
	}
	if !s.lobby.HasSubs(env.UserID) {
		return
	}
	s.lobby.Deliver(env.UserID, Event{Type: "match_found", Payload: env.Payload})
}

// enqueueMatchmake puts the caller in the matchmaking queue. The
// player count is taken from the request body; mode is derived from
// the count and the caller's rating is looked up server-side so a
// client cannot misrepresent itself. Idempotent — second click just
// refreshes enqueued_at via the table's ON CONFLICT DO UPDATE.
func (s *Server) enqueueMatchmake(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	var req LobbyEnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Players < 2 || req.Players > 6 {
		writeError(w, http.StatusBadRequest, "players must be in [2, 6]")
		return
	}
	mode := RatingModeMulti
	if req.Players == 2 {
		mode = RatingMode1v1
	}
	rating := elo.DefaultRating
	if rated, err := s.store.Repo().RatingFor(r.Context(), u.ID, mode); err == nil && rated.Games > 0 {
		rating = rated.Rating
	}
	if err := s.store.Enqueue(r.Context(), u.ID, req.Players, mode, rating); err != nil {
		s.log.Error("enqueue matchmake", "err", err)
		writeError(w, http.StatusInternalServerError, "could not enqueue")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":  true,
		"players": req.Players,
		"mode":    mode,
	})
}

// cancelMatchmake removes the caller's ticket. Always returns 204 so
// the client doesn't need to special-case "I wasn't queued" vs "I was
// just matched 50ms ago and got removed before this call landed".
func (s *Server) cancelMatchmake(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}
	if err := s.store.CancelMatchmake(r.Context(), u.ID); err != nil {
		s.log.Error("cancel matchmake", "err", err)
		writeError(w, http.StatusInternalServerError, "could not cancel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wsLobby serves the user's lobby WebSocket. The connection stays
// open while the user is in queue; when the matcher pairs them, a
// "match_found" event flows down and the client navigates to the
// freshly-created game.
//
// On any close (matched, cancelled, navigated away), we call
// CancelMatchmake. After a successful match the queue row is already
// gone (the matcher DELETEd it in its tx), so the call is a no-op —
// but it's the safety net for "user closed their tab while still in
// queue", which would otherwise leave a stuck row until the future
// cleanup job runs.
func (s *Server) wsLobby(w http.ResponseWriter, r *http.Request) {
	u := requireUser(w, r)
	if u == nil {
		return
	}

	var opts websocket.AcceptOptions
	if len(s.allowedOrigins) == 0 {
		opts.InsecureSkipVerify = true
	} else {
		opts.OriginPatterns = s.allowedOrigins
	}
	conn, err := websocket.Accept(w, r, &opts)
	if err != nil {
		s.log.Warn("ws lobby accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	sub := s.lobby.Subscribe(u.ID)
	defer s.lobby.Unsubscribe(u.ID, sub)
	// Best-effort cleanup. If the user navigated away with a pending
	// ticket, this drops it so the next tick doesn't try to pair a
	// phantom user. Use Background — we don't want the request ctx
	// (already cancelled by the close) to abort the cleanup query.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
		defer cancel()
		_ = s.store.CancelMatchmake(ctx, u.ID)
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader goroutine: we don't expect any messages from the lobby
	// client today, but reading is required to detect a close (pings
	// + handshakes flow through Read). Anything we receive is
	// discarded.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-sub.ch:
			if !ok {
				return
			}
			writeCtx, cancelWrite := context.WithTimeout(ctx, wsWriteTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, msg)
			cancelWrite()
			if err != nil {
				return
			}

		case <-pingTicker.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, wsPingTimeout)
			err := conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				return
			}
		}
	}
}


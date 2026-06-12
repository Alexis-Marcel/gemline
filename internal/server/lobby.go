package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alexis/gemline/internal/elo"
	"github.com/coder/websocket"
)

// LobbyEnqueueRequest is the POST /api/matchmake/enqueue body. Mode is derived
// server-side from Players so a client can't pick its rating bucket.
type LobbyEnqueueRequest struct {
	Players int `json:"players"`
}

// LobbyMatchPayload is emitted over the lobby WS when the matcher pairs the
// user. The client uses gameId to redirect and token to authenticate the seat
// without a follow-up join call.
type LobbyMatchPayload struct {
	GameID    string `json:"gameId"`
	Token     string `json:"token"`
	SeatIndex int    `json:"seatIndex"`
	Name      string `json:"name"`
}

// LobbyInvitePayload is sent when someone reserves a seat for the user
// (invite_received) or withdraws it (invite_cancelled).
type LobbyInvitePayload struct {
	GameID    string `json:"gameId"`
	SeatIndex int    `json:"seatIndex"`
	// FromName / FromUserID identify the host; empty when the source can't
	// be identified (e.g. anonymous host).
	FromName   string `json:"fromName,omitempty"`
	FromUserID string `json:"fromUserId,omitempty"`
}

// Lobby event type discriminators. Constants so backplane envelopes and the WS
// write side use the exact same strings.
const (
	LobbyEventMatchFound      = "match_found"
	LobbyEventInviteReceived  = "invite_received"
	LobbyEventInviteCancelled = "invite_cancelled"
	LobbyEventQueueUpdate     = "queue_update"
)

// LobbyQueueUpdatePayload is the per-tick signal pushed to queued users.
// ETASeconds is omitted for 1v1 and under-quorum multi buckets.
type LobbyQueueUpdatePayload struct {
	Players    int  `json:"players"`
	Count      int  `json:"count"`
	ETASeconds *int `json:"etaSeconds,omitempty"`
}

// lobbyEnvelope is the JSON shape sent through ChannelLobby. UserID is the
// routing key; Payload is RawMessage so a payload shape change doesn't require
// updating intermediaries.
type lobbyEnvelope struct {
	UserID  string          `json:"userId"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// fanMatched is the matcher's onMatched callback: one NOTIFY per matched seat.
// Failures are logged but not retried — the user is in the game regardless, and
// a page nav will hit the new game.
func (s *Server) fanMatched(seats []MatchedSeat) {
	for _, seat := range seats {
		s.publishLobby(seat.UserID, LobbyEventMatchFound, LobbyMatchPayload{
			GameID:    seat.GameID,
			Token:     seat.Token,
			SeatIndex: seat.SeatIndex,
			Name:      seat.Name,
		})
	}
}

// fanQueueUpdate is the matcher's onQueueUpdate callback: forwards one
// queue_update event per still-queued user.
func (s *Server) fanQueueUpdate(updates []QueueUpdate) {
	for _, u := range updates {
		s.publishLobby(u.UserID, LobbyEventQueueUpdate, LobbyQueueUpdatePayload{
			Players:    u.Players,
			Count:      u.Count,
			ETASeconds: u.ETASeconds,
		})
	}
}

// publishLobby fans a lobby event to one user, via the backplane when present
// (multi-pod NOTIFY) or directly through the in-process LobbyHub otherwise.
func (s *Server) publishLobby(userID, eventType string, payload any) {
	if userID == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		s.log.Error("lobby publish: marshal payload", "type", eventType, "err", err)
		return
	}
	if s.backplane == nil {
		// Single-process: skip the wire format and go straight to the hub.
		s.lobby.Deliver(userID, Event{Type: eventType, Payload: json.RawMessage(body)})
		return
	}
	env, err := json.Marshal(lobbyEnvelope{
		UserID:  userID,
		Type:    eventType,
		Payload: body,
	})
	if err != nil {
		s.log.Error("lobby publish: marshal envelope", "type", eventType, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := s.backplane.Publish(ctx, ChannelLobby, env); err != nil {
		s.log.Error("lobby publish: notify", "user", userID, "type", eventType, "err", err)
	}
}

// handleLobbyNotif is the ChannelLobby handler. Each pod receives every
// notification; routing by userID happens locally — pods without a sub no-op.
func (s *Server) handleLobbyNotif(payload []byte) {
	var env lobbyEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		s.log.Warn("lobby notif: bad envelope", "err", err)
		return
	}
	if env.Type == "" {
		// Backwards-compat: older envelopes had no type field. Default to
		// match_found so a half-deployed cluster doesn't drop matches.
		env.Type = LobbyEventMatchFound
	}
	if !s.lobby.HasSubs(env.UserID) {
		return
	}
	s.lobby.Deliver(env.UserID, Event{Type: env.Type, Payload: env.Payload})
}

// enqueueMatchmake puts the caller in the matchmaking queue. Mode and rating
// are resolved server-side so a client cannot misrepresent itself.
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

// cancelMatchmake removes the caller's ticket. Always returns 204 so the client
// needn't distinguish "wasn't queued" from "just matched and already removed".
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

// wsLobby serves the persistent per-user WebSocket carrying cross-page
// notifications (match_found, invite_received / invite_cancelled).
//
// Close does NOT cancel a pending matchmaking ticket: with a persistent socket,
// "close" usually means a network blip. Cancellation happens via the explicit
// HTTP endpoint; orphaned queue rows are caught by the next matcher tick and the
// periodic stale cleaner.
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

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader goroutine: we expect no client messages, but reading is required
	// to detect a close (pings + handshakes flow through Read). Discard all.
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

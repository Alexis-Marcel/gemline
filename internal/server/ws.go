package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	wsWriteTimeout = 5 * time.Second
	wsPingInterval = 25 * time.Second
	wsPingTimeout  = 10 * time.Second
)

// wsClientMessage is anything the client may send to us. Only `hello` is
// handled today; new branches can land on Type without breaking compat.
type wsClientMessage struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
}

func (s *Server) wsGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.log.Error("ws load game", "err", err)
		http.Error(w, "could not load game", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Origin policy mirrors the HTTP CORS middleware:
	//   - allow-list set      → use OriginPatterns so the websocket library
	//                           rejects unknown origins.
	//   - allow-list empty    → dev-permissive (skip the check entirely). The
	//                           server logs a warning at boot when this is
	//                           the case.
	var opts websocket.AcceptOptions
	if len(s.allowedOrigins) == 0 {
		opts.InsecureSkipVerify = true
	} else {
		opts.OriginPatterns = s.allowedOrigins
	}
	conn, err := websocket.Accept(w, r, &opts)
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	sub := s.hub.Subscribe(id)
	defer s.hub.Unsubscribe(id, sub)

	rec.Lock()
	snapshot := toGameDTO(rec)
	rec.Unlock()
	// Tag the initial snapshot with the current event_seq so the client
	// can detect catch-up gaps on reconnect (compare to its last-seen
	// seq before this open) and dedup any live events that arrive with
	// seq <= this value.
	seq, err := s.store.Repo().CurrentEventSeq(r.Context(), id)
	if err != nil {
		s.log.Warn("ws current seq", "game", id, "err", err)
	}
	s.sendEvent(conn, Event{Type: "state", Seq: seq, Payload: snapshot})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader goroutine: dispatches client messages. The "hello" message
	// registers this connection's seat for presence tracking. Authentication
	// can arrive any time after open — the client may resend it after a
	// reconnect, for example.
	go s.runReader(ctx, conn, rec, cancel)

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
			// A protocol-level ping forces the peer to pong; if no pong arrives
			// before the timeout we treat the connection as dead. This catches
			// half-open TCP and idle-proxy disconnects that don't surface as a
			// clean close.
			pingCtx, cancelPing := context.WithTimeout(ctx, wsPingTimeout)
			err := conn.Ping(pingCtx)
			cancelPing()
			if err != nil {
				s.log.Info("ws ping failed, closing", "game", id, "err", err)
				return
			}
		}
	}
}

// runReader owns the connection's read side: dispatches client messages and
// tracks the seat assigned via "hello". When the read loop exits (closed or
// errored) and the connection had registered a seat, we report it as
// disconnected so the presence timer kicks in.
func (s *Server) runReader(ctx context.Context, conn *websocket.Conn, rec *GameRecord, cancel context.CancelFunc) {
	defer cancel()
	seatIndex := -1
	defer func() {
		if seatIndex >= 0 {
			s.store.SeatDisconnected(rec.ID, seatIndex)
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg wsClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "hello" && seatIndex < 0 && msg.Token != "" {
			rec.Lock()
			seat, ok := rec.SeatByToken(msg.Token)
			rec.Unlock()
			if ok {
				seatIndex = seat.Index
				s.store.SeatConnected(rec.ID, seatIndex)
			}
		}
	}
}

func (s *Server) sendEvent(conn *websocket.Conn, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

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

func (s *Server) wsGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin enforced by reverse proxy / CORS for now
	})
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
	s.sendEvent(conn, eventState(snapshot))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader goroutine: the protocol is broadcast-only for now, but we still
	// need to read so the library can process control frames (pong replies to
	// our pings, close frames, etc).
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

func (s *Server) sendEvent(conn *websocket.Conn, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

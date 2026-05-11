package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) wsGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec, ok := s.store.Get(id)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin check handled by CORS for now
	})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	sub := s.hub.Subscribe(id)
	defer s.hub.Unsubscribe(id, sub)

	// Send initial snapshot.
	rec.Lock()
	snapshot := toGameDTO(rec)
	rec.Unlock()
	s.sendEvent(conn, eventState(snapshot))

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Reader goroutine: we mostly care about closes; client → server traffic
	// goes through the REST endpoints so the WS is broadcast-only for now.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.ch:
			if !ok {
				return
			}
			writeCtx, cancelWrite := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, msg)
			cancelWrite()
			if err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

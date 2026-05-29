package server

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// Event is what gets pushed over the WebSocket. Seq is the per-game sequence
// from game_events (0 and elided for transient events); clients use it to
// detect catch-up gaps on reconnect.
type Event struct {
	Type    string      `json:"type"`
	Seq     int         `json:"seq,omitempty"`
	Payload interface{} `json:"payload"`
}

func eventState(g gameDTO) Event {
	return Event{Type: "state", Payload: g}
}

func eventMove(mr moveResponse) Event {
	return Event{Type: "move", Payload: mr}
}

func eventChat(m Message) Event {
	return Event{Type: "chat", Payload: m}
}

// eventRated mirrors GET /api/games/:id/ratings so the client applies it as a
// drop-in replacement for the data it fetched on game-end.
func eventRated(gr GameRatings) Event {
	return Event{Type: "rated", Payload: gr}
}

type presencePayload struct {
	SeatIndex int  `json:"seatIndex"`
	Online    bool `json:"online"`
}

func eventPresence(seatIndex int, online bool) Event {
	return Event{Type: "presence", Payload: presencePayload{SeatIndex: seatIndex, Online: online}}
}

// subscriber holds one client's channel. A full buffer drops messages rather
// than blocking the hub.
type subscriber struct {
	ch chan []byte
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{}
	log  *slog.Logger
	kind string // tags the wsConnections gauge ("game" or "lobby")
}

func NewHub(log *slog.Logger, kind string) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		subs: make(map[string]map[*subscriber]struct{}),
		log:  log,
		kind: kind,
	}
}

func (h *Hub) Subscribe(gameID string) *subscriber {
	sub := &subscriber{ch: make(chan []byte, 16)}
	h.mu.Lock()
	if h.subs[gameID] == nil {
		h.subs[gameID] = make(map[*subscriber]struct{})
	}
	h.subs[gameID][sub] = struct{}{}
	h.mu.Unlock()
	if h.kind != "" {
		wsConnections.WithLabelValues(h.kind).Inc()
	}
	return sub
}

func (h *Hub) Unsubscribe(gameID string, sub *subscriber) {
	h.mu.Lock()
	if set, ok := h.subs[gameID]; ok {
		delete(set, sub)
		if len(set) == 0 {
			delete(h.subs, gameID)
		}
	}
	h.mu.Unlock()
	close(sub.ch)
	if h.kind != "" {
		wsConnections.WithLabelValues(h.kind).Dec()
	}
}

// Deliver fans an event out to local subscribers of gameID. Don't call it to
// broadcast — use EventPublisher.Publish, which persists the event and triggers
// the NOTIFY that ends up calling Deliver on every pod.
func (h *Hub) Deliver(gameID string, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		// A marshal failure means a DTO regression (non-marshalable payload
		// field); log it rather than dropping silently.
		h.log.Error("hub deliver: marshal event", "game", gameID, "type", ev.Type, "err", err)
		return
	}
	dropped := 0
	h.mu.RLock()
	for sub := range h.subs[gameID] {
		select {
		case sub.ch <- b:
		default:
			dropped++
		}
	}
	h.mu.RUnlock()
	if dropped > 0 {
		// A steady drop count means a client can't keep up; dropping is policy,
		// so warn rather than error.
		h.log.Warn("hub deliver: subscribers dropped event", "game", gameID, "type", ev.Type, "dropped", dropped)
	}
}

// HasSubs reports whether any subscriber is registered for gameID. The backplane
// listener uses it to skip the game_events SELECT for games it doesn't serve.
func (h *Hub) HasSubs(gameID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[gameID]) > 0
}

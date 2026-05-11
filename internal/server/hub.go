package server

import (
	"encoding/json"
	"sync"
)

// Event is what gets pushed over the WebSocket. Type is a discriminator,
// payload depends on it.
type Event struct {
	Type    string      `json:"type"`
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

type presencePayload struct {
	SeatIndex int  `json:"seatIndex"`
	Online    bool `json:"online"`
}

func eventPresence(seatIndex int, online bool) Event {
	return Event{Type: "presence", Payload: presencePayload{SeatIndex: seatIndex, Online: online}}
}

// subscriber holds the channel a single client reads from. Sending blocks
// when the buffer is full; we drop messages instead of blocking the hub.
type subscriber struct {
	ch chan []byte
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscriber]struct{})}
}

func (h *Hub) Subscribe(gameID string) *subscriber {
	sub := &subscriber{ch: make(chan []byte, 16)}
	h.mu.Lock()
	if h.subs[gameID] == nil {
		h.subs[gameID] = make(map[*subscriber]struct{})
	}
	h.subs[gameID][sub] = struct{}{}
	h.mu.Unlock()
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
}

func (h *Hub) Broadcast(gameID string, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subs[gameID] {
		select {
		case sub.ch <- b:
		default:
			// drop on full buffer: a slow client must not block the hub
		}
	}
}

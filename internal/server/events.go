package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/alexis/gemline/internal/backplane"
)

// EventPublisher is the single fan-out path for WS events. Publish persists the
// event (allocating a per-game seq), then NOTIFYs {gameId, seq, podId}; local
// fan-out happens in the backplane handler, so the publishing pod receives its
// own NOTIFY like every other pod — one symmetric path. With the noop repo
// (no DATABASE_URL) it falls back to a direct hub.Deliver.
type EventPublisher struct {
	repo       Repository
	hub        *Hub
	backplane  *backplane.Backplane
	log        *slog.Logger
	podID      string
	invalidate func(gameID string) // nil disables cross-pod cache invalidation
}

func NewEventPublisher(repo Repository, hub *Hub, bp *backplane.Backplane, log *slog.Logger, podID string, invalidate func(string)) *EventPublisher {
	if log == nil {
		log = slog.Default()
	}
	return &EventPublisher{
		repo:       repo,
		hub:        hub,
		backplane:  bp,
		log:        log,
		podID:      podID,
		invalidate: invalidate,
	}
}

// notifyEnvelope stays tiny: listeners read the event row back from the DB, so
// the payload never approaches the 8 KB NOTIFY cap. PodID lets a receiver skip
// invalidating its cache for self-originated events.
type notifyEnvelope struct {
	GameID string `json:"gameId"`
	Seq    int    `json:"seq"`
	PodID  string `json:"podId,omitempty"`
}

const publishTimeout = 5 * time.Second

// Publish persists ev and triggers the cross-pod notification. It takes no
// context on purpose: the domain change already committed, so the broadcast
// must go out even if the caller's request ended. Failures are logged, not
// returned — clients refill missed events on reconnect.
func (p *EventPublisher) Publish(gameID string, ev Event) {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		p.log.Error("publish: marshal payload", "game", gameID, "type", ev.Type, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	seq, err := p.repo.AppendEvent(ctx, gameID, ev.Type, payload)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			return // game deleted under us
		}
		p.log.Error("publish: append event", "game", gameID, "type", ev.Type, "err", err)
		p.hub.Deliver(gameID, ev) // local delivery beats a silent miss
		return
	}

	// noopRepo path (no DATABASE_URL): deliver in-process.
	if seq == 0 || p.backplane == nil {
		ev.Seq = seq
		p.hub.Deliver(gameID, ev)
		return
	}

	env, _ := json.Marshal(notifyEnvelope{GameID: gameID, Seq: seq, PodID: p.podID})
	if err := p.backplane.Publish(ctx, ChannelGameEvents, env); err != nil {
		p.log.Error("publish: notify", "game", gameID, "seq", seq, "err", err)
		// Bus down: deliver locally; other pods resync via /events?since=N.
		ev.Seq = seq
		p.hub.Deliver(gameID, ev)
	}
}

const (
	ChannelGameEvents = "gemline_events"
	ChannelLobby      = "gemline_lobby"
)

// HandleGameEventNotif handles ChannelGameEvents: invalidate the local cache on
// cross-pod notifications (so the next Get reloads), then deliver to local WS
// subscribers if any. Invalidation runs even with no subscribers — a later HTTP
// read/write must see fresh state — while the DB round-trip is skipped when
// nobody is watching.
func (p *EventPublisher) HandleGameEventNotif(payload []byte) {
	var env notifyEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		p.log.Warn("listener: bad envelope", "err", err)
		return
	}

	if env.PodID != "" && env.PodID != p.podID && p.invalidate != nil {
		p.invalidate(env.GameID)
	}

	if !p.hub.HasSubs(env.GameID) {
		return
	}
	row, err := p.repo.LoadEvent(context.Background(), env.GameID, env.Seq)
	if err != nil {
		p.log.Error("listener: load event", "game", env.GameID, "seq", env.Seq, "err", err)
		return
	}
	if row.Seq == 0 {
		return // deleted by retention before we read it
	}
	p.hub.Deliver(env.GameID, Event{
		Type:    row.Type,
		Seq:     row.Seq,
		Payload: row.Payload,
	})
}

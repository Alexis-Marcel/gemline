package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/alexis/gemline/internal/backplane"
)

// EventPublisher is the only thing that should fan events out to WS
// clients. Calling Publish:
//
//  1. Persists the event in game_events (atomically allocating the next
//     per-game seq).
//  2. Emits a NOTIFY on 'gemline_events' carrying just {gameId, seq,
//     podId}.
//
// Local fan-out happens in the backplane's notification handler — the
// publishing pod receives its own NOTIFY back the same way every other
// pod does. One code path, symmetric across pods, ~5ms slower locally
// than a direct in-process broadcast. The simplicity is worth the
// latency.
//
// PodID lets the listener distinguish "I sent this" from "someone else
// sent this", which matters for the cache-invalidation hook: we only
// drop the local cached rec when a NOTIFY originates from another pod
// (the publishing pod's in-memory state is already consistent with
// what it just persisted).
//
// When the Repository is the noop (no DATABASE_URL set, in tests or
// dev), AppendEvent returns seq=0 and Publish falls back to a direct
// hub.Deliver so single-process runs still surface live updates.
type EventPublisher struct {
	repo       Repository
	hub        *Hub
	backplane  *backplane.Backplane
	log        *slog.Logger
	podID      string
	invalidate func(gameID string) // optional; nil disables cross-pod cache invalidation
}

// NewEventPublisher wires together the canonical store (for AppendEvent
// and LoadEvent), the local hub (for fan-out delivery in the single-
// process fallback), and the bus (for NOTIFY emission). If backplane is
// nil — which is the case when no DATABASE_URL is configured — Publish
// degrades to direct hub.Deliver and no cross-pod propagation happens.
//
// podID is this process's identity; it travels in every NOTIFY so the
// listener handler can tell self-originated events apart and skip the
// cache invalidation in that case. invalidate is the callback used to
// drop the local Store cache for a game when a NOTIFY arrives from a
// different pod; pass nil to disable invalidation (e.g. in single-pod
// or test setups where there's only one cache to begin with).
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

// notifyEnvelope is the JSON shape sent through 'gemline_events'. The
// payload is small by design — listeners read the event row back from
// the DB, so we never bump against the 8 KB NOTIFY payload cap no matter
// how large a state DTO grows.
//
// PodID identifies the originating process; receiving pods compare it
// to their own to decide whether to invalidate their cached rec for
// the game (we skip self-invalidation because the publisher's in-memory
// state is already consistent with what it just persisted).
type notifyEnvelope struct {
	GameID string `json:"gameId"`
	Seq    int    `json:"seq"`
	PodID  string `json:"podId,omitempty"`
}

// publishTimeout caps how long we'll wait on the DB + bus for any one
// Publish call. The HTTP handler that triggered the publish has already
// committed its domain change by the time we get here, and we don't
// want a slow bus to hold the response goroutine.
const publishTimeout = 5 * time.Second

// Publish persists ev for gameID and triggers a cross-pod notification.
// The original ev struct is left unchanged; the seq assigned by the DB
// flows back through the listener so every pod (including the caller's)
// delivers a fully-tagged Event to its local subscribers.
//
// Publish deliberately does not take a context: the caller's request
// may end before persistence completes (network blip, client closed),
// but the event broadcast must still go out because the domain change
// has already committed. We pin a fresh Background+timeout internally.
//
// Persistence and notification failures are logged but not returned —
// clients will refill any missed events from the canonical tables on
// their next reconnect.
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
			// Race: caller tried to publish for a game that's been
			// deleted out from under it. Not noteworthy.
			return
		}
		p.log.Error("publish: append event", "game", gameID, "type", ev.Type, "err", err)
		// Fall through to local delivery so users on this pod still see
		// the change — better than a silent miss when the bus is down.
		p.hub.Deliver(gameID, ev)
		return
	}

	// noopRepo path (no DATABASE_URL): no real persistence, no
	// backplane. Deliver in-process so single-pod dev runs still work.
	if seq == 0 || p.backplane == nil {
		ev.Seq = seq
		p.hub.Deliver(gameID, ev)
		return
	}

	env, _ := json.Marshal(notifyEnvelope{GameID: gameID, Seq: seq, PodID: p.podID})
	if err := p.backplane.Publish(ctx, ChannelGameEvents, env); err != nil {
		p.log.Error("publish: notify", "game", gameID, "seq", seq, "err", err)
		// Bus failed: deliver locally so this pod's subscribers still
		// see the event. Other pods miss it; their clients resync on
		// reconnect via /api/games/:id/events?since=N.
		ev.Seq = seq
		p.hub.Deliver(gameID, ev)
	}
}

// Channel names the backplane listens on. Exported so wiring in
// cmd/server/main.go and server.go can refer to them by symbol rather
// than literal strings.
const (
	ChannelGameEvents = "gemline_events"
	ChannelLobby      = "gemline_lobby"
)

// HandleGameEventNotif is the backplane handler for ChannelGameEvents.
// On every notification it parses the envelope, invalidates the local
// Store cache when the notification comes from another pod (so the
// next Get reloads fresh state), and delivers the event to local WS
// subscribers when there are any.
//
// Two things are deliberately decoupled:
//
//  1. Cache invalidation happens for every cross-pod notification,
//     regardless of whether we currently have WS subscribers for the
//     game. A pod with no subs might still serve an HTTP read or
//     write for the game later, and we want that to see fresh state.
//
//  2. Hub.Deliver only runs when HasSubs reports interest. A pod with
//     no local subscribers skips the LoadEvent round-trip entirely;
//     the invalidation has already happened by then.
//
// Wired via backplane.Subscribe(ChannelGameEvents, …) in Server.New.
func (p *EventPublisher) HandleGameEventNotif(payload []byte) {
	var env notifyEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		p.log.Warn("listener: bad envelope", "err", err)
		return
	}

	// Cross-pod notif: drop our cached *GameRecord for this game so
	// any subsequent Get reloads from the canonical store. Skipping
	// self-originated notifs avoids a useless reload of data we just
	// wrote.
	if env.PodID != "" && env.PodID != p.podID && p.invalidate != nil {
		p.invalidate(env.GameID)
	}

	if !p.hub.HasSubs(env.GameID) {
		// Nobody on this pod is watching that game — the NOTIFY is
		// for some other pod's WS subs. Skip the DB round-trip
		// entirely; we've already done the bookkeeping that matters
		// for non-WS code paths (the invalidation above).
		return
	}
	row, err := p.repo.LoadEvent(context.Background(), env.GameID, env.Seq)
	if err != nil {
		p.log.Error("listener: load event", "game", env.GameID, "seq", env.Seq, "err", err)
		return
	}
	if row.Seq == 0 {
		// Row missing — probably deleted by retention before we got
		// here. Nothing to deliver.
		return
	}
	p.hub.Deliver(env.GameID, Event{
		Type:    row.Type,
		Seq:     row.Seq,
		Payload: row.Payload, // json.RawMessage; passes through json.Marshal verbatim
	})
}

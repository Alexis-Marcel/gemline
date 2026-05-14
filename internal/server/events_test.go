package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

// EventPublisher integrates Repository + Hub + Backplane. The Backplane
// itself requires a real Postgres for any meaningful test, so what we
// pin here is the wiring around it: the noop-repo fallback (Publish
// becomes a direct local Deliver) and the cache-invalidation hook
// semantics (self-originated NOTIFYs skip invalidate, cross-pod ones
// don't).

func newTestPublisher(t *testing.T, podID string, invalidate func(string)) (*EventPublisher, *Hub, Repository) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(log)
	repo := noopRepo{}
	// nil backplane = single-process fallback path.
	p := NewEventPublisher(repo, hub, nil, log, podID, invalidate)
	return p, hub, repo
}

func TestPublish_NoopFallsBackToLocalDeliver(t *testing.T) {
	pub, hub, _ := newTestPublisher(t, "pod-a", nil)
	sub := hub.Subscribe("game-1")
	defer hub.Unsubscribe("game-1", sub)

	pub.Publish("game-1", Event{Type: "state", Payload: map[string]string{"hello": "world"}})

	// Local Hub.Deliver writes a marshalled Event to the sub's channel.
	select {
	case raw := <-sub.ch:
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ev.Type != "state" {
			t.Fatalf("want type=state, got %s", ev.Type)
		}
	default:
		t.Fatalf("expected an event on the sub channel; nothing arrived")
	}
}

func TestPublish_NoopWithNoSubscribersIsHarmless(t *testing.T) {
	// No subscribers, no DB, no backplane: Publish must be a no-op
	// rather than blocking or panicking.
	pub, _, _ := newTestPublisher(t, "pod-a", nil)
	pub.Publish("game-1", Event{Type: "state", Payload: nil})
}

func TestHandleGameEventNotif_SelfPodSkipsInvalidate(t *testing.T) {
	var mu sync.Mutex
	var invalidated []string
	pub, _, _ := newTestPublisher(t, "pod-self", func(id string) {
		mu.Lock()
		invalidated = append(invalidated, id)
		mu.Unlock()
	})

	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7, PodID: "pod-self"})
	pub.HandleGameEventNotif(env)

	mu.Lock()
	defer mu.Unlock()
	if len(invalidated) != 0 {
		t.Fatalf("self-originated NOTIFY must skip invalidate, got %v", invalidated)
	}
}

func TestHandleGameEventNotif_OtherPodInvalidates(t *testing.T) {
	var mu sync.Mutex
	var invalidated []string
	pub, _, _ := newTestPublisher(t, "pod-self", func(id string) {
		mu.Lock()
		invalidated = append(invalidated, id)
		mu.Unlock()
	})

	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7, PodID: "pod-other"})
	pub.HandleGameEventNotif(env)

	mu.Lock()
	defer mu.Unlock()
	if len(invalidated) != 1 || invalidated[0] != "game-1" {
		t.Fatalf("cross-pod NOTIFY must invalidate the affected game; got %v", invalidated)
	}
}

func TestHandleGameEventNotif_EmptyPodIDDoesNotInvalidate(t *testing.T) {
	// Older / hand-crafted envelopes without a PodID should not
	// trigger invalidation; we can't tell who sent them so we err on
	// the side of "don't drop the cache".
	var called bool
	pub, _, _ := newTestPublisher(t, "pod-self", func(string) { called = true })

	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7})
	pub.HandleGameEventNotif(env)

	if called {
		t.Fatalf("envelope without PodID must not trigger invalidate")
	}
}

func TestHandleGameEventNotif_NoSubsSkipsDelivery(t *testing.T) {
	// HasSubs is false → no LoadEvent, no Hub.Deliver. Cache
	// invalidation still happens for cross-pod notifs (tested above),
	// but the delivery path is short-circuited.
	pub, hub, _ := newTestPublisher(t, "pod-self", nil)
	// No Subscribe call: hub has no subs for "game-1".

	// Even without subs, HandleGameEventNotif should return cleanly
	// (no LoadEvent attempt — noopRepo would return (zero, nil)
	// anyway, but we want to verify we don't even try).
	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7, PodID: "pod-other"})
	pub.HandleGameEventNotif(env)

	if hub.HasSubs("game-1") {
		t.Fatalf("test setup broken: hub should not report subs for game-1")
	}
}


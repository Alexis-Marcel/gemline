package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

// The Backplane needs a real Postgres, so these tests pin the wiring around it:
// the noop-repo fallback (Publish becomes a local Deliver) and the
// cache-invalidation rule (self-originated NOTIFYs skip invalidate, cross-pod
// ones don't).

func newTestPublisher(t *testing.T, podID string, invalidate func(string)) (*EventPublisher, *Hub, Repository) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(log, "")
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
	// No subs, no DB, no backplane: Publish must not block or panic.
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
	// Envelopes without a PodID: sender is unknown, so err toward keeping
	// the cache rather than invalidating.
	var called bool
	pub, _, _ := newTestPublisher(t, "pod-self", func(string) { called = true })

	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7})
	pub.HandleGameEventNotif(env)

	if called {
		t.Fatalf("envelope without PodID must not trigger invalidate")
	}
}

func TestHandleGameEventNotif_NoSubsSkipsDelivery(t *testing.T) {
	// HasSubs false → delivery is short-circuited (no LoadEvent, no Deliver);
	// cross-pod invalidation still runs (covered above).
	pub, hub, _ := newTestPublisher(t, "pod-self", nil)

	env, _ := json.Marshal(notifyEnvelope{GameID: "game-1", Seq: 7, PodID: "pod-other"})
	pub.HandleGameEventNotif(env)

	if hub.HasSubs("game-1") {
		t.Fatalf("test setup broken: hub should not report subs for game-1")
	}
}

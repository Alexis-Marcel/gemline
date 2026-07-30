package bus

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// collector accumulates payloads thread-safely and lets tests wait for them.
type collector struct {
	mu   sync.Mutex
	msgs []string
}

func (c *collector) add(p []byte) {
	c.mu.Lock()
	c.msgs = append(c.msgs, string(p))
	c.mu.Unlock()
}

func (c *collector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// waitFor polls until cond or the deadline; pub/sub delivery is asynchronous
// so assertions on arrival need a grace window.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached before deadline")
}

func newTestBus(t *testing.T, addr string) (*Redis, *collector, *collector) {
	t.Helper()
	b, err := NewRedis("redis://"+addr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	games, lobby := &collector{}, &collector{}
	b.OnGameEvent(games.add)
	b.OnLobby(lobby.add)
	t.Cleanup(func() { _ = b.Close() })
	return b, games, lobby
}

func TestPerGameRouting(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()

	watcher, watcherGames, _ := newTestBus(t, mr.Addr())
	bystander, bystanderGames, _ := newTestBus(t, mr.Addr())
	watcher.WatchGame("g1")
	watcher.Start(ctx)
	bystander.Start(ctx)

	publisher, _, _ := newTestBus(t, mr.Addr())
	if err := publisher.PublishGame(ctx, "g1", []byte("ev-g1")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, func() bool { return len(watcherGames.snapshot()) == 1 })
	if got := watcherGames.snapshot()[0]; got != "ev-g1" {
		t.Fatalf("watcher got %q", got)
	}
	// The bystander watches nothing: the event must not have reached it.
	// (Give delivery a moment so absence is meaningful.)
	time.Sleep(100 * time.Millisecond)
	if got := bystanderGames.snapshot(); len(got) != 0 {
		t.Fatalf("bystander received %v — per-game routing is broken", got)
	}
}

func TestLobbyIsBroadcast(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()

	a, _, aLobby := newTestBus(t, mr.Addr())
	b, _, bLobby := newTestBus(t, mr.Addr())
	a.Start(ctx)
	b.Start(ctx)

	if err := a.PublishLobby(ctx, []byte("match")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, func() bool { return len(aLobby.snapshot()) == 1 && len(bLobby.snapshot()) == 1 })
}

func TestWatchBeforeStartAndDynamicWatch(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()

	sub, games, _ := newTestBus(t, mr.Addr())
	sub.WatchGame("early") // declared before Start: must subscribe on open
	sub.Start(ctx)
	sub.WatchGame("late") // declared after Start: dynamic subscribe

	pub, _, _ := newTestBus(t, mr.Addr())
	// Dynamic subscribe is async; retry until both channels deliver.
	waitFor(t, func() bool {
		_ = pub.PublishGame(ctx, "early", []byte("e"))
		_ = pub.PublishGame(ctx, "late", []byte("l"))
		seen := map[string]bool{}
		for _, m := range games.snapshot() {
			seen[m] = true
		}
		return seen["e"] && seen["l"]
	})
}

func TestUnwatchIsRefcounted(t *testing.T) {
	mr := miniredis.RunT(t)
	ctx := context.Background()

	sub, games, _ := newTestBus(t, mr.Addr())
	sub.Start(ctx)
	pub, _, _ := newTestBus(t, mr.Addr())

	// Two interests (cache + WS spectator); dropping one must keep delivery.
	sub.WatchGame("g")
	sub.WatchGame("g")
	sub.UnwatchGame("g")
	waitFor(t, func() bool {
		_ = pub.PublishGame(ctx, "g", []byte("still"))
		return len(games.snapshot()) >= 1
	})

	// Dropping the last interest must stop delivery.
	sub.UnwatchGame("g")
	time.Sleep(50 * time.Millisecond) // let the unsubscribe land
	before := len(games.snapshot())
	_ = pub.PublishGame(ctx, "g", []byte("gone"))
	time.Sleep(100 * time.Millisecond)
	if after := len(games.snapshot()); after != before {
		t.Fatalf("received %d events after last unwatch", after-before)
	}
}

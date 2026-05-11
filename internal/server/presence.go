package server

import (
	"context"
	"sync"
	"time"
)

// DisconnectGracePeriod is how long a seat may stay disconnected before its
// player forfeits the game. Independent from the chess clock — typically
// much shorter so a game doesn't stall.
const DisconnectGracePeriod = 60 * time.Second

// presenceKey identifies a (game, seat) pair for the timer registry.
type presenceKey struct {
	gameID    string
	seatIndex int
}

// presenceManager owns per-seat disconnect-grace timers. At most one timer
// per (game, seat) at a time.
type presenceManager struct {
	mu      sync.Mutex
	cancels map[presenceKey]context.CancelFunc
}

func newPresenceManager() *presenceManager {
	return &presenceManager{cancels: make(map[presenceKey]context.CancelFunc)}
}

// Schedule starts a disconnect-grace timer for the seat. If a timer already
// existed for the same key, it is cancelled first.
func (pm *presenceManager) Schedule(gameID string, seatIndex int, grace time.Duration, onTimeout func()) {
	key := presenceKey{gameID, seatIndex}
	pm.cancelLocked(key)

	ctx, cancel := context.WithCancel(context.Background())
	pm.mu.Lock()
	pm.cancels[key] = cancel
	pm.mu.Unlock()

	go func() {
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			onTimeout()
		}
	}()
}

// Cancel stops any pending timer for (gameID, seatIndex). No-op if none.
func (pm *presenceManager) Cancel(gameID string, seatIndex int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.cancelLocked(presenceKey{gameID, seatIndex})
}

// CancelGame stops every pending timer for `gameID`. Used when the game
// ends (any reason) so we don't fire a stale forfeit.
func (pm *presenceManager) CancelGame(gameID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for k := range pm.cancels {
		if k.gameID == gameID {
			pm.cancelLocked(k)
		}
	}
}

// CancelAll stops every pending timer; called on server shutdown.
func (pm *presenceManager) CancelAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for k := range pm.cancels {
		pm.cancelLocked(k)
	}
}

// cancelLocked must be called with pm.mu held.
func (pm *presenceManager) cancelLocked(key presenceKey) {
	if cancel, ok := pm.cancels[key]; ok {
		cancel()
		delete(pm.cancels, key)
	}
}

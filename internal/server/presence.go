package server

import (
	"context"
	"sync"
	"time"
)

// DisconnectGracePeriod is how long a seat may stay disconnected before it
// forfeits — independent of (and usually shorter than) the chess clock.
const DisconnectGracePeriod = 60 * time.Second

type presenceKey struct {
	gameID    string
	seatIndex int
}

// presenceManager owns at most one disconnect-grace timer per (game, seat).
type presenceManager struct {
	mu      sync.Mutex
	cancels map[presenceKey]context.CancelFunc
}

func newPresenceManager() *presenceManager {
	return &presenceManager{cancels: make(map[presenceKey]context.CancelFunc)}
}

// Schedule starts (or replaces) the grace timer for a seat. Cancel-and-install
// is atomic under pm.mu so concurrent Schedules can't leak a goroutine.
func (pm *presenceManager) Schedule(gameID string, seatIndex int, grace time.Duration, onTimeout func()) {
	key := presenceKey{gameID, seatIndex}
	ctx, cancel := context.WithCancel(context.Background())
	pm.mu.Lock()
	pm.cancelLocked(key)
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

func (pm *presenceManager) Cancel(gameID string, seatIndex int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.cancelLocked(presenceKey{gameID, seatIndex})
}

// CancelGame stops every timer for gameID, so a finished game can't fire a
// stale forfeit.
func (pm *presenceManager) CancelGame(gameID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for k := range pm.cancels {
		if k.gameID == gameID {
			pm.cancelLocked(k)
		}
	}
}

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

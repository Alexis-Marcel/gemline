package server

import (
	"context"
	"sync"
	"time"
)

// clockManager owns the per-game timer that fires when the active player's
// chess clock runs out. There's at most one timer alive per game at any
// time; (re)scheduling cancels the previous one.
type clockManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newClockManager() *clockManager {
	return &clockManager{cancels: make(map[string]context.CancelFunc)}
}

// Schedule fires `onFlag` after `remaining` if no Cancel/Schedule call
// supersedes it first. A negative or zero `remaining` invokes onFlag
// immediately on a background goroutine.
func (cm *clockManager) Schedule(gameID string, remaining time.Duration, onFlag func()) {
	cm.Cancel(gameID)
	ctx, cancel := context.WithCancel(context.Background())
	cm.mu.Lock()
	cm.cancels[gameID] = cancel
	cm.mu.Unlock()

	go func() {
		if remaining <= 0 {
			onFlag()
			return
		}
		t := time.NewTimer(remaining)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			onFlag()
		}
	}()
}

// Cancel stops the current timer for `gameID`, if any.
func (cm *clockManager) Cancel(gameID string) {
	cm.mu.Lock()
	if cancel, ok := cm.cancels[gameID]; ok {
		cancel()
		delete(cm.cancels, gameID)
	}
	cm.mu.Unlock()
}

// CancelAll stops every active timer. Used on server shutdown.
func (cm *clockManager) CancelAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, cancel := range cm.cancels {
		cancel()
		delete(cm.cancels, id)
	}
}

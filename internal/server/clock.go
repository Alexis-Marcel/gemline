package server

import (
	"context"
	"sync"
	"time"
)

// clockManager owns at most one timer per game; (re)scheduling cancels the
// previous one.
type clockManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newClockManager() *clockManager {
	return &clockManager{cancels: make(map[string]context.CancelFunc)}
}

// Schedule fires onFlag after `remaining` unless a later Cancel/Schedule
// supersedes it first (<=0 fires immediately). Cancelling the previous timer
// and installing the new one happen atomically under cm.mu, so two concurrent
// Schedules can't both install a timer and leak a goroutine.
func (cm *clockManager) Schedule(gameID string, remaining time.Duration, onFlag func()) {
	ctx, cancel := context.WithCancel(context.Background())
	cm.mu.Lock()
	if prev, ok := cm.cancels[gameID]; ok {
		prev()
	}
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

func (cm *clockManager) Cancel(gameID string) {
	cm.mu.Lock()
	if cancel, ok := cm.cancels[gameID]; ok {
		cancel()
		delete(cm.cancels, gameID)
	}
	cm.mu.Unlock()
}

func (cm *clockManager) CancelAll() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for id, cancel := range cm.cancels {
		cancel()
		delete(cm.cancels, id)
	}
}

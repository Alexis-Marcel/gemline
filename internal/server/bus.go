package server

import "context"

// Bus is the cross-pod fan-out (Redis pub/sub, internal/bus). Game events are
// routed per game: a pod subscribes to game:{id} only while it has local
// interest, so its inbound traffic scales with what it serves. The bus is
// lossy by design — envelopes carry {gameId, seq}, and consumers reconcile
// from the canonical store via seq catch-up. Nil bus (tests, no DATABASE_URL)
// falls back to direct local delivery.
type Bus interface {
	PublishGame(ctx context.Context, gameID string, payload []byte) error
	PublishLobby(ctx context.Context, payload []byte) error
	// PublishMatchmake rings the matchmaker's doorbell after an enqueue, so
	// the elected matcher ticks immediately instead of on its next interval.
	PublishMatchmake(ctx context.Context) error
	// Handler registration; call before Start.
	OnGameEvent(fn func(payload []byte))
	OnLobby(fn func(payload []byte))
	OnMatchmake(fn func())
	// WatchGame/UnwatchGame declare local interest (refcounted): a cached
	// record and a first WS spectator each count as one.
	WatchGame(gameID string)
	UnwatchGame(gameID string)
	Start(ctx context.Context)
	Close() error
}

package server

import (
	"context"

	"github.com/alexis-marcel/gemline/internal/backplane"
)

// Bus abstracts cross-pod fan-out. Implementations differ in routing, not in
// contract: the Postgres backplane broadcasts game events on one global
// channel (every pod receives everything and filters), the Redis bus routes
// per game (only pods watching a game receive its events). Both are lossy —
// the envelope carries {gameId, seq}, and consumers reconcile from the
// canonical store, so handlers are implementation-agnostic.
type Bus interface {
	PublishGame(ctx context.Context, gameID string, payload []byte) error
	PublishLobby(ctx context.Context, payload []byte) error
	// Handler registration; call before Start.
	OnGameEvent(fn func(payload []byte))
	OnLobby(fn func(payload []byte))
	// WatchGame/UnwatchGame declare local interest (refcounted): a cached
	// record and a first WS spectator each count as one. The global backplane
	// ignores them — it already receives everything.
	WatchGame(gameID string)
	UnwatchGame(gameID string)
	Start(ctx context.Context)
	Close() error
}

// pgBus adapts the LISTEN/NOTIFY backplane to Bus: game routing degrades to
// the global channel, interest declarations are no-ops.
type pgBus struct{ bp *backplane.Backplane }

func NewPostgresBus(bp *backplane.Backplane) Bus { return &pgBus{bp: bp} }

func (b *pgBus) PublishGame(ctx context.Context, _ string, payload []byte) error {
	return b.bp.Publish(ctx, ChannelGameEvents, payload)
}
func (b *pgBus) PublishLobby(ctx context.Context, payload []byte) error {
	return b.bp.Publish(ctx, ChannelLobby, payload)
}
func (b *pgBus) OnGameEvent(fn func(payload []byte)) { b.bp.Subscribe(ChannelGameEvents, fn) }
func (b *pgBus) OnLobby(fn func(payload []byte))     { b.bp.Subscribe(ChannelLobby, fn) }
func (b *pgBus) WatchGame(string)                    {}
func (b *pgBus) UnwatchGame(string)                  {}
func (b *pgBus) Start(ctx context.Context)           { b.bp.Start(ctx) }
func (b *pgBus) Close() error                        { return b.bp.Close() }

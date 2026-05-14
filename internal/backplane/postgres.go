// Package backplane provides a Postgres-backed pub/sub for cross-pod
// notifications. It is the wake-up channel that lets every pod know
// something happened in the canonical state (game_events row inserted,
// matchmake_queue ticket created…), so they can fan out to whatever
// local subscribers they hold.
//
// The backplane is intentionally narrow: it knows about channels and
// byte payloads, nothing about games, matchmaking, or events. Anything
// domain-specific lives one layer up in package server.
//
// Connection model:
//
//   - LISTEN runs on a *dedicated* long-lived pgx connection, because
//     database/sql's pool can hand any query to any connection and
//     LISTEN is session-scoped — you cannot LISTEN through a pool.
//   - NOTIFY goes through the existing *sql.DB pool. NOTIFY is just an
//     SQL statement with no follow-up, so the pool model is fine.
//
// Failure model:
//
//   - The listener auto-reconnects on transient errors. Missed
//     notifications during the gap are not replayed — the bus is
//     considered lossy by design, and consumers are expected to
//     reconcile from the canonical store (game_events, …) on
//     reconnect.
package backplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Handler is invoked once per inbound notification on a channel.
// Handlers run on the listener's single goroutine; long work should be
// dispatched elsewhere so we don't hold up the next notification.
type Handler func(payload []byte)

// Backplane is a Postgres LISTEN/NOTIFY bus. It is safe for concurrent
// use after New returns.
type Backplane struct {
	dsn  string
	pool *sql.DB
	log  *slog.Logger

	mu       sync.RWMutex
	handlers map[string][]Handler

	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a Backplane. It does not open the listener
// connection yet — call Start once every Subscribe has been
// registered, otherwise the corresponding LISTEN statement won't run
// until the next reconnect.
//
// The passed *sql.DB is reused for NOTIFY. The caller owns the pool's
// lifecycle; Close on the Backplane only tears down the listener.
func New(dsn string, pool *sql.DB, log *slog.Logger) *Backplane {
	return &Backplane{
		dsn:      dsn,
		pool:     pool,
		log:      log,
		handlers: make(map[string][]Handler),
	}
}

// Subscribe registers handler to be invoked every time a NOTIFY
// arrives on channel. Multiple handlers per channel are allowed and
// are called in registration order. Must be called before Start —
// adding subscriptions after the listener is running will only take
// effect on the next reconnect.
func (b *Backplane) Subscribe(channel string, handler Handler) {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], handler)
	b.mu.Unlock()
}

// Start launches the receive loop. The loop runs until ctx is
// cancelled or Close is called. Returns immediately; errors from the
// session loop are logged but not surfaced because we always retry.
func (b *Backplane) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.done = make(chan struct{})
	go b.run(runCtx)
}

// Publish emits a NOTIFY on channel with payload. It runs on the
// pool's connection — fire-and-forget, no session state left behind.
// The payload is sent through pg_notify(text, text) rather than the
// SQL-level NOTIFY statement, because the SQL statement requires an
// identifier and forbids parameter binding for the channel name.
func (b *Backplane) Publish(ctx context.Context, channel string, payload []byte) error {
	_, err := b.pool.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, string(payload))
	if err != nil {
		return fmt.Errorf("pg_notify %s: %w", channel, err)
	}
	return nil
}

// Close cancels the receive loop and waits for it to return. Safe to
// call before Start (it's a no-op). The pool passed to New is left
// untouched — Close is for the listener only.
func (b *Backplane) Close() error {
	if b.cancel == nil {
		return nil
	}
	b.cancel()
	<-b.done
	return nil
}

// run is the receive loop: connect, LISTEN to every registered channel,
// WaitForNotification in a loop, dispatch to handlers. On any error
// (broken conn, server restart) it sleeps briefly and retries. The
// loop exits only when the context is cancelled.
func (b *Backplane) run(ctx context.Context) {
	defer close(b.done)

	const reconnectDelay = 2 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := b.session(ctx); err != nil && !errors.Is(err, context.Canceled) {
			b.log.Error("backplane session ended", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// session opens one pgx connection, runs LISTEN for every known
// channel, then loops on WaitForNotification until error or ctx done.
// Returns the terminating error so run() can decide whether to retry.
func (b *Backplane) session(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, b.dsn)
	if err != nil {
		return fmt.Errorf("pgx connect: %w", err)
	}
	defer conn.Close(context.Background())

	b.mu.RLock()
	channels := make([]string, 0, len(b.handlers))
	for ch := range b.handlers {
		channels = append(channels, ch)
	}
	b.mu.RUnlock()

	for _, ch := range channels {
		// LISTEN takes a quoted identifier and rejects parameter
		// binding, so we re-quote channel ourselves. The set of
		// channels is controlled by us (server boot wires them up),
		// not by user input, so the static format is safe.
		if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %q", ch)); err != nil {
			return fmt.Errorf("listen %s: %w", ch, err)
		}
	}
	b.log.Info("backplane listening", "channels", channels)

	for {
		notif, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		b.dispatch(notif.Channel, []byte(notif.Payload))
	}
}

// dispatch invokes every handler registered for channel with payload.
// Handlers are called synchronously on the listener goroutine — they
// must not block on long operations.
func (b *Backplane) dispatch(channel string, payload []byte) {
	b.mu.RLock()
	handlers := b.handlers[channel]
	b.mu.RUnlock()
	for _, h := range handlers {
		h(payload)
	}
}

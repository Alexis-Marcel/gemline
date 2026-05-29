// Package backplane is a Postgres LISTEN/NOTIFY pub/sub for cross-pod wake-ups.
// It deals only in channels and byte payloads; domain logic lives in package
// server.
//
// LISTEN runs on a dedicated long-lived pgx connection because it is
// session-scoped and can't go through database/sql's pool; NOTIFY goes through
// the pool since it's a single statement with no session state.
//
// The bus is lossy by design: the listener auto-reconnects but drops
// notifications during the gap, so consumers reconcile from the canonical store
// on reconnect.
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

// Handler is invoked once per inbound notification. Handlers run on the
// listener's single goroutine; dispatch long work elsewhere.
type Handler func(payload []byte)

// Backplane is a Postgres LISTEN/NOTIFY bus, safe for concurrent use after New.
type Backplane struct {
	dsn  string
	pool *sql.DB
	log  *slog.Logger

	mu       sync.RWMutex
	handlers map[string][]Handler

	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a Backplane without opening the listener; call Start after all
// Subscribe calls. The *sql.DB is reused for NOTIFY and owned by the caller —
// Close only tears down the listener.
func New(dsn string, pool *sql.DB, log *slog.Logger) *Backplane {
	return &Backplane{
		dsn:      dsn,
		pool:     pool,
		log:      log,
		handlers: make(map[string][]Handler),
	}
}

// Subscribe registers a handler for channel, called in registration order.
// Must be called before Start; later additions only take effect on the next
// reconnect.
func (b *Backplane) Subscribe(channel string, handler Handler) {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], handler)
	b.mu.Unlock()
}

// Start launches the receive loop until ctx is cancelled or Close is called.
// Returns immediately; session errors are logged, not surfaced, since we retry.
func (b *Backplane) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.done = make(chan struct{})
	go b.run(runCtx)
}

// Publish emits a NOTIFY on channel via the pool (fire-and-forget). It uses
// pg_notify(text, text) rather than the SQL NOTIFY statement, which forbids
// parameter binding for the channel name.
func (b *Backplane) Publish(ctx context.Context, channel string, payload []byte) error {
	_, err := b.pool.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, string(payload))
	if err != nil {
		return fmt.Errorf("pg_notify %s: %w", channel, err)
	}
	return nil
}

// Close cancels the receive loop and waits for it to return. No-op before
// Start; the pool passed to New is left untouched.
func (b *Backplane) Close() error {
	if b.cancel == nil {
		return nil
	}
	b.cancel()
	<-b.done
	return nil
}

// run drives sessions, sleeping briefly and retrying on any error. Exits only
// when ctx is cancelled.
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

// session opens one pgx connection, LISTENs on every channel, then loops on
// WaitForNotification until error or ctx done, returning the terminating error.
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
		// LISTEN needs a quoted identifier and rejects parameter binding, so
		// we quote it ourselves. Channel names are server-controlled, not user
		// input, so the format is safe.
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

// dispatch invokes every handler for channel synchronously on the listener
// goroutine; handlers must not block.
func (b *Backplane) dispatch(channel string, payload []byte) {
	b.mu.RLock()
	handlers := b.handlers[channel]
	b.mu.RUnlock()
	for _, h := range handlers {
		h(payload)
	}
}

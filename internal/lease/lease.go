// Package lease implements per-game ownership leases on Postgres: at most one
// pod owns a live game at a time. A lease is a lock that expires — liveness is
// proven by heartbeat renewal, so a crashed owner is superseded after the TTL
// without any cleanup. The epoch increments on every change of hands and is
// the fencing token later stamped on writes.
package lease

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

// DefaultTTL trades takeover latency after a crash (players wait this long for
// their clock to revive) against tolerance for a few missed heartbeats.
const DefaultTTL = 15 * time.Second

// Manager acquires, renews and releases this pod's leases. Safe for concurrent
// use after New.
type Manager struct {
	pool  *sql.DB
	owner string
	ttl   time.Duration
	log   *slog.Logger

	mu   sync.Mutex
	held map[string]int64 // gameID → epoch this pod holds

	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(pool *sql.DB, owner string, log *slog.Logger) *Manager {
	return &Manager{
		pool:  pool,
		owner: owner,
		ttl:   DefaultTTL,
		log:   log,
		held:  make(map[string]int64),
	}
}

// WithTTL overrides the lease TTL (tests shorten it to exercise expiry).
func (m *Manager) WithTTL(d time.Duration) *Manager {
	m.ttl = d
	return m
}

// NewOwnerID returns "<hostname>-<hex>": the hostname maps a lease to a pod in
// k8s, the random suffix disambiguates local processes sharing one hostname.
func NewOwnerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "pod"
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return host + "-" + hex.EncodeToString(b)
}

func (m *Manager) Owner() string { return m.owner }

// Held reports whether this pod believes it owns gameID, and at which epoch.
// Belief can be stale by up to one heartbeat; correctness must come from the
// epoch check on writes (step 1c), never from this.
func (m *Manager) Held(gameID string) (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	epoch, ok := m.held[gameID]
	return epoch, ok
}

// The WHERE clause makes the upsert succeed in exactly three cases: the lease
// is free (plain insert), already ours (renewal, epoch unchanged), or expired
// (takeover, epoch+1). A valid lease held by someone else matches nothing and
// returns no row. Concurrent calls serialize on the row lock, so exactly one
// contender wins a takeover.
const acquireSQL = `
INSERT INTO game_leases (game_id, owner_id, epoch, expires_at)
VALUES ($1, $2, 1, NOW() + make_interval(secs => $3))
ON CONFLICT (game_id) DO UPDATE
   SET owner_id   = EXCLUDED.owner_id,
       epoch      = game_leases.epoch
                    + CASE WHEN game_leases.owner_id = EXCLUDED.owner_id THEN 0 ELSE 1 END,
       expires_at = EXCLUDED.expires_at
 WHERE game_leases.owner_id = EXCLUDED.owner_id
    OR game_leases.expires_at < NOW()
RETURNING epoch`

// TryAcquire attempts to take (or renew) the lease on gameID. Returns
// acquired=false without error when a live lease is held by another pod.
func (m *Manager) TryAcquire(ctx context.Context, gameID string) (epoch int64, acquired bool, err error) {
	err = m.pool.QueryRowContext(ctx, acquireSQL, gameID, m.owner, m.ttl.Seconds()).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	m.mu.Lock()
	m.held[gameID] = epoch
	m.mu.Unlock()
	return epoch, true, nil
}

// CurrentOwner returns the pod holding a live lease on gameID, or "" if the
// lease is free or expired.
func (m *Manager) CurrentOwner(ctx context.Context, gameID string) (string, error) {
	var owner string
	err := m.pool.QueryRowContext(ctx,
		`SELECT owner_id FROM game_leases WHERE game_id = $1 AND expires_at >= NOW()`, gameID,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

// EnsureHeld acquires the lease on gameID unless this pod already holds it,
// logging the outcome. Failure (held elsewhere, or DB error) is observational
// only and must never block serving the game.
func (m *Manager) EnsureHeld(ctx context.Context, gameID string) {
	if _, ok := m.Held(gameID); ok {
		return
	}
	epoch, acquired, err := m.TryAcquire(ctx, gameID)
	if err != nil {
		m.log.Warn("lease acquire failed", "game", gameID, "err", err)
		return
	}
	if acquired {
		m.log.Info("lease acquired", "game", gameID, "epoch", epoch, "owner", m.owner)
		return
	}
	other, err := m.CurrentOwner(ctx, gameID)
	if err != nil {
		other = "unknown"
	}
	m.log.Info("lease held elsewhere", "game", gameID, "owner", other)
}

// Release drops the lease so another pod can take it immediately instead of
// waiting out the TTL. Owner-scoped DELETE: releasing a lease we already lost
// is a no-op rather than stealing it back.
func (m *Manager) Release(ctx context.Context, gameID string) {
	if _, err := m.pool.ExecContext(ctx,
		`DELETE FROM game_leases WHERE game_id = $1 AND owner_id = $2`, gameID, m.owner,
	); err != nil {
		m.log.Warn("lease release failed", "game", gameID, "err", err)
	}
	m.mu.Lock()
	delete(m.held, gameID)
	m.mu.Unlock()
}

// Start launches the heartbeat loop until ctx is cancelled or Close is called.
func (m *Manager) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.run(runCtx)
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	// Renew at ttl/3 so a lease survives two consecutive missed heartbeats
	// before anyone may take it over.
	ticker := time.NewTicker(m.ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.renew(ctx)
		}
	}
}

// renew extends every lease still owned by this pod in one statement, then
// reconciles: a held lease missing from the result was taken over (we blew
// past our expiry) and must be forgotten, not fought for.
func (m *Manager) renew(ctx context.Context) {
	rows, err := m.pool.QueryContext(ctx,
		`UPDATE game_leases SET expires_at = NOW() + make_interval(secs => $1)
		  WHERE owner_id = $2 RETURNING game_id`, m.ttl.Seconds(), m.owner)
	if err != nil {
		// Transient DB errors are survivable: the TTL absorbs ttl/3 * 2 of
		// failed renewals before ownership is actually at risk.
		m.log.Warn("lease heartbeat failed", "err", err)
		return
	}
	defer rows.Close()
	renewed := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			m.log.Warn("lease heartbeat scan", "err", err)
			return
		}
		renewed[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		m.log.Warn("lease heartbeat", "err", err)
		return
	}

	m.mu.Lock()
	for id := range m.held {
		if _, ok := renewed[id]; !ok {
			delete(m.held, id)
			m.log.Warn("lease lost", "game", id)
		}
	}
	m.mu.Unlock()
}

// Close stops the heartbeat and releases every lease this pod holds, so a
// clean shutdown hands games over immediately instead of after the TTL.
// Expiry remains the safety net for unclean deaths.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
		<-m.done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := m.pool.ExecContext(ctx, `DELETE FROM game_leases WHERE owner_id = $1`, m.owner)
	if err != nil {
		m.log.Warn("lease release-all failed", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.log.Info("leases released", "count", n)
	}
	m.mu.Lock()
	m.held = make(map[string]int64)
	m.mu.Unlock()
}

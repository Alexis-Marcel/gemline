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
	addr  string
	ttl   time.Duration
	log   *slog.Logger

	mu   sync.Mutex
	held map[string]int64 // gameID → epoch this pod holds

	// onLost fires (from the heartbeat goroutine) for each lease found taken
	// over during renewal — the owner's cue to stand down its timers.
	onLost func(gameID string)

	cancel context.CancelFunc
	done   chan struct{}
}

// SetOnLost registers the lease-lost callback. Must be called before Start;
// the callback must not block (it runs on the heartbeat goroutine).
func (m *Manager) SetOnLost(fn func(gameID string)) { m.onLost = fn }

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

// WithAddr sets the internal address advertised in this pod's leases, where
// sibling pods forward game commands. Empty disables forwarding to this pod.
func (m *Manager) WithAddr(addr string) *Manager {
	m.addr = addr
	return m
}

func (m *Manager) Addr() string { return m.addr }

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

// Resolver is the read-only view of the lease table, for pods that never own
// games (gateways): who owns a game and where to reach it, nothing else.
type Resolver struct{ pool *sql.DB }

func NewResolver(pool *sql.DB) *Resolver { return &Resolver{pool: pool} }

// Owner returns the pod holding a live lease on gameID and its internal
// address, or ("", "") when the lease is free or expired.
func (r *Resolver) Owner(ctx context.Context, gameID string) (owner, addr string, err error) {
	err = r.pool.QueryRowContext(ctx,
		`SELECT owner_id, owner_addr FROM game_leases WHERE game_id = $1 AND expires_at >= NOW()`, gameID,
	).Scan(&owner, &addr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return owner, addr, err
}

// Grant is the outcome of an acquisition attempt: either we hold the lease at
// Epoch, or Owner/Addr identify who does.
type Grant struct {
	Epoch    int64
	Acquired bool
	Owner    string // set when not acquired
	Addr     string
}

// The upsert's WHERE clause makes it succeed in exactly three cases: the lease
// is free (plain insert), already ours (renewal, epoch unchanged), or expired
// (takeover, epoch+1). A valid lease held by someone else matches nothing —
// then the second branch reports the current holder, all in one round-trip.
// Concurrent calls serialize on the row lock, so exactly one contender wins a
// takeover.
const acquireSQL = `
WITH attempt AS (
    INSERT INTO game_leases (game_id, owner_id, owner_addr, epoch, expires_at)
    VALUES ($1, $2, $3, 1, NOW() + make_interval(secs => $4))
    ON CONFLICT (game_id) DO UPDATE
       SET owner_id   = EXCLUDED.owner_id,
           owner_addr = EXCLUDED.owner_addr,
           epoch      = game_leases.epoch
                        + CASE WHEN game_leases.owner_id = EXCLUDED.owner_id THEN 0 ELSE 1 END,
           expires_at = EXCLUDED.expires_at
     WHERE game_leases.owner_id = EXCLUDED.owner_id
        OR game_leases.expires_at < NOW()
    RETURNING epoch
)
SELECT epoch, TRUE, '', '' FROM attempt
UNION ALL
SELECT l.epoch, FALSE, l.owner_id, l.owner_addr
  FROM game_leases l
 WHERE l.game_id = $1 AND NOT EXISTS (SELECT 1 FROM attempt)`

// Acquire attempts to take (or renew) the lease on gameID; when a live lease
// is held by another pod it returns that holder instead, without error.
//
// Rare edge: two pods inserting the very first lease row simultaneously — the
// loser sees neither its attempt nor the winner's row (statement snapshot
// predates it) and gets a zero Grant. Callers treat "not acquired, no owner"
// as handle-locally, which is the safe degradation.
func (m *Manager) Acquire(ctx context.Context, gameID string) (Grant, error) {
	var g Grant
	err := m.pool.QueryRowContext(ctx, acquireSQL, gameID, m.owner, m.addr, m.ttl.Seconds()).
		Scan(&g.Epoch, &g.Acquired, &g.Owner, &g.Addr)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, nil
	}
	if err != nil {
		return Grant{}, err
	}
	if g.Acquired {
		m.mu.Lock()
		m.held[gameID] = g.Epoch
		m.mu.Unlock()
	}
	return g, nil
}

// TryAcquire is Acquire without the holder details.
func (m *Manager) TryAcquire(ctx context.Context, gameID string) (epoch int64, acquired bool, err error) {
	g, err := m.Acquire(ctx, gameID)
	return g.Epoch, g.Acquired, err
}

// EnsureHeld acquires the lease on gameID unless this pod already holds it,
// logging the outcome. Failure (held elsewhere, or DB error) is observational
// only and must never block serving the game.
func (m *Manager) EnsureHeld(ctx context.Context, gameID string) {
	if _, ok := m.Held(gameID); ok {
		return
	}
	g, err := m.Acquire(ctx, gameID)
	if err != nil {
		m.log.Warn("lease acquire failed", "game", gameID, "err", err)
		return
	}
	if g.Acquired {
		m.log.Info("lease acquired", "game", gameID, "epoch", g.Epoch, "owner", m.owner)
		return
	}
	m.log.Info("lease held elsewhere", "game", gameID, "owner", g.Owner)
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

	var lost []string
	m.mu.Lock()
	for id := range m.held {
		if _, ok := renewed[id]; !ok {
			delete(m.held, id)
			lost = append(lost, id)
		}
	}
	m.mu.Unlock()
	// Invoke outside the lock: the callback may call back into Held.
	for _, id := range lost {
		m.log.Warn("lease lost", "game", id)
		if m.onLost != nil {
			m.onLost(id)
		}
	}
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

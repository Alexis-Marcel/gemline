package lease

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Elector is leader election over a leader_leases row: every candidate
// campaigns on a timer, exactly one holds the lease at a time, and the others
// take over when it stops renewing (crash) or deletes the row (clean resign).
// The elected callback runs with a context that is cancelled the moment
// leadership is lost — the leader's workload must stop with it.
type Elector struct {
	pool  *sql.DB
	name  string // the elected role, e.g. "matchmaker"
	owner string
	ttl   time.Duration
	log   *slog.Logger

	onElected func(ctx context.Context)

	mu          sync.Mutex
	epoch       int64
	leadCancel  context.CancelFunc // non-nil while leading

	cancel context.CancelFunc
	done   chan struct{}
}

func NewElector(pool *sql.DB, name, owner string, log *slog.Logger) *Elector {
	return &Elector{
		pool:  pool,
		name:  name,
		owner: owner,
		ttl:   DefaultTTL,
		log:   log,
	}
}

// WithTTL overrides the leadership TTL (tests shorten it).
func (e *Elector) WithTTL(d time.Duration) *Elector {
	e.ttl = d
	return e
}

// OnElected registers the leader workload. Must be set before Start. The
// callback is launched in its own goroutine on every leadership gain and must
// exit when its context is cancelled.
func (e *Elector) OnElected(fn func(ctx context.Context)) { e.onElected = fn }

// IsLeader reports current belief; stale by up to one heartbeat, like Held.
func (e *Elector) IsLeader() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leadCancel != nil
}

// Same three-case upsert as game leases: free, ours (renew), or expired
// (takeover, epoch+1).
const campaignSQL = `
INSERT INTO leader_leases (name, owner_id, epoch, expires_at)
VALUES ($1, $2, 1, NOW() + make_interval(secs => $3))
ON CONFLICT (name) DO UPDATE
   SET owner_id   = EXCLUDED.owner_id,
       epoch      = leader_leases.epoch
                    + CASE WHEN leader_leases.owner_id = EXCLUDED.owner_id THEN 0 ELSE 1 END,
       expires_at = EXCLUDED.expires_at
 WHERE leader_leases.owner_id = EXCLUDED.owner_id
    OR leader_leases.expires_at < NOW()
RETURNING epoch`

// Start launches the campaign loop until ctx is cancelled or Close is called.
// Campaigning is continuous: a follower keeps trying (the takeover path), a
// leader keeps renewing (the heartbeat path) — one statement serves both.
func (e *Elector) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		// Deposed when the loop exits for any reason: the workload must not
		// outlive the campaign that justifies it.
		defer e.stepDown()
		ticker := time.NewTicker(e.ttl / 3)
		defer ticker.Stop()
		e.campaign(runCtx)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				e.campaign(runCtx)
			}
		}
	}()
}

func (e *Elector) campaign(ctx context.Context) {
	var epoch int64
	err := e.pool.QueryRowContext(ctx, campaignSQL, e.name, e.owner, e.ttl.Seconds()).Scan(&epoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A live leader elsewhere. If we thought we led, we've been deposed.
		e.stepDown()
	case err != nil:
		// Transient DB errors don't depose us by themselves: the TTL absorbs
		// two missed heartbeats, same policy as game leases.
		e.log.Warn("election campaign failed", "role", e.name, "err", err)
	default:
		e.becomeLeader(ctx, epoch)
	}
}

func (e *Elector) becomeLeader(ctx context.Context, epoch int64) {
	e.mu.Lock()
	if e.leadCancel != nil {
		e.epoch = epoch
		e.mu.Unlock()
		return // renewal, already leading
	}
	leadCtx, cancel := context.WithCancel(ctx)
	e.leadCancel = cancel
	e.epoch = epoch
	fn := e.onElected
	e.mu.Unlock()

	e.log.Info("leadership acquired", "role", e.name, "owner", e.owner, "epoch", epoch)
	if fn != nil {
		go fn(leadCtx)
	}
}

func (e *Elector) stepDown() {
	e.mu.Lock()
	cancel := e.leadCancel
	e.leadCancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
		e.log.Warn("leadership lost", "role", e.name, "owner", e.owner)
	}
}

// Close stops campaigning and resigns: the owner-scoped DELETE hands
// leadership over immediately instead of after the TTL. Expiry remains the
// safety net for unclean deaths.
func (e *Elector) Close() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.pool.ExecContext(ctx,
		`DELETE FROM leader_leases WHERE name = $1 AND owner_id = $2`, e.name, e.owner,
	); err != nil {
		e.log.Warn("leadership resign failed", "role", e.name, "err", err)
	}
}

package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const maxRequestBytes = 32 << 10

func maxBytesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimiter holds per-key token buckets. Limits are per-pod — enough to stop
// floods, not a precise distributed quota.
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*rlEntry
	lastSweep time.Time
}

type rlEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*rlEntry), lastSweep: time.Now()}
}

func (rl *rateLimiter) allow(key string, r rate.Limit, burst int) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if now.Sub(rl.lastSweep) > time.Minute {
		for k, e := range rl.buckets {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastSweep = now
	}
	e := rl.buckets[key]
	if e == nil {
		e = &rlEntry{lim: rate.NewLimiter(r, burst)}
		rl.buckets[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

// limited wraps a handler with a named per-caller rate limit. The caller is the
// authed user when present, else the client IP.
func (s *Server) limited(name string, r rate.Limit, burst int, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !s.limiter.allow(name+"|"+rateKey(req), r, burst) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "trop de requêtes, réessaie dans un instant")
			return
		}
		h(w, req)
	}
}

func rateKey(r *http.Request) string {
	if u, ok := userFromContext(r.Context()); ok {
		return "u:" + u.ID
	}
	return "ip:" + clientIP(r)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

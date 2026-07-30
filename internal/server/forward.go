package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type forwardCtxKey struct{}

// markForwarded tags every request arriving on the internal listener, so
// owned() always handles it locally. This is the loop guard: an ownership
// change mid-flight degrades to the pre-affinity behavior (DB serialization)
// instead of ping-ponging between pods. The tag lives in the context, not a
// header, so external clients cannot spoof it — the internal listener simply
// isn't reachable through the ingress.
func markForwarded(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), forwardCtxKey{}, true)))
	})
}

func isForwarded(r *http.Request) bool {
	return r.Context().Value(forwardCtxKey{}) != nil
}

// forwardTransport is shared across all proxied commands: sibling pods are
// few, so pooled keep-alive connections cover the traffic.
var forwardTransport = &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	MaxIdleConnsPerHost:   16,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 10 * time.Second,
}

// owned routes a game command to the game's owner: handled locally when this
// pod holds (or can claim) the lease, proxied to the live owner's internal
// listener otherwise. Every fallback (no manager, DB error, ownerless lease,
// addr-less or unreachable owner) is "handle locally" — affinity is an
// optimization layer, never a reason to fail a command. Local handling without
// the lease is safe: it writes with epoch 0 (unfenced) under the same DB
// serialization as before affinity existed.
func (s *Server) owned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lm := s.store.leases
		gameID := r.PathValue("id")
		if lm == nil || gameID == "" || isForwarded(r) {
			next(w, r)
			return
		}
		if _, held := lm.Held(gameID); held {
			next(w, r)
			return
		}
		// One round-trip: claim the lease if free or expired, or learn who
		// holds it.
		grant, err := lm.Acquire(r.Context(), gameID)
		if err != nil || grant.Acquired || grant.Addr == "" || grant.Addr == lm.Addr() {
			next(w, r)
			return
		}

		// Buffer the body (capped upstream at 32 KiB) so an unreachable owner
		// lets us replay the request locally instead of failing the command.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.proxyTo(w, r, gameID, grant.Owner, grant.Addr, func() {
			r.Body = io.NopCloser(bytes.NewReader(body))
			next(w, r)
		})
	}
}

// proxyTo relays the command to the owner's internal listener. retryLocal runs
// on transport-level failure (dial refused, header timeout) — errors raised
// before any response bytes were written, so replaying locally is safe. A
// failure mid-response aborts instead (the command may have executed).
func (s *Server) proxyTo(w http.ResponseWriter, r *http.Request, gameID, owner, addr string, retryLocal func()) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Preserve the real client IP: the owner's rate limiter keys
			// anonymous callers on it, and must not collapse everyone this
			// pod forwards onto one bucket.
			pr.SetXForwarded()
		},
		Transport: forwardTransport,
		ErrorHandler: func(_ http.ResponseWriter, _ *http.Request, err error) {
			s.log.Warn("forward failed, handling locally", "game", gameID, "owner", owner, "err", err)
			retryLocal()
		},
	}
	s.log.Info("command forwarded", "game", gameID, "owner", owner, "path", r.URL.Path)
	proxy.ServeHTTP(w, r)
}

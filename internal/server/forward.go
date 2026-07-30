package server

import (
	"context"
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
// addr-less owner) is "handle locally" — affinity is an optimization layer,
// never a reason to fail a command.
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
		// Not ours: claim it if free or expired; a live lease elsewhere makes
		// TryAcquire return acquired=false and we forward instead.
		if _, acquired, err := lm.TryAcquire(r.Context(), gameID); acquired || err != nil {
			next(w, r)
			return
		}
		owner, addr, err := lm.CurrentOwner(r.Context(), gameID)
		if err != nil || addr == "" || addr == lm.Addr() {
			next(w, r)
			return
		}
		s.proxyTo(w, r, gameID, owner, addr)
	}
}

func (s *Server) proxyTo(w http.ResponseWriter, r *http.Request, gameID, owner, addr string) {
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
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			s.log.Warn("command forward failed", "game", gameID, "owner", owner, "err", err)
			writeError(w, http.StatusBadGateway, "le pod propriétaire de la partie est injoignable")
		},
	}
	s.log.Info("command forwarded", "game", gameID, "owner", owner, "path", r.URL.Path)
	proxy.ServeHTTP(w, r)
}

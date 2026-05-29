package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Cardinality stays bounded because `route` is the http.ServeMux *pattern*
// (e.g. `GET /api/games/{id}`), not the concrete path — distinct game IDs
// collapse to a single label value.
var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gemline_http_requests_total",
		Help: "Total HTTP requests, partitioned by method, route and status.",
	}, []string{"method", "route", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gemline_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, partitioned by method, route and status.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status"})

	wsConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gemline_websocket_connections",
		Help: "Active WebSocket connections, partitioned by hub kind.",
	}, []string{"hub"})

	// Business metrics. Labels stay low-cardinality: `players` ∈ [2..6],
	// `outcome` ∈ {resign, draw, timeout, win}, `actor` ∈ {human, bot}.
	gamesCreatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gemline_games_created_total",
		Help: "Total games created, partitioned by player count and visibility.",
	}, []string{"players", "visibility"})

	gamesFinishedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gemline_games_finished_total",
		Help: "Total games that reached a terminal state, partitioned by outcome.",
	}, []string{"outcome"})

	movesPlayedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gemline_moves_played_total",
		Help: "Total moves played, partitioned by actor (human or bot).",
	}, []string{"actor"})

	// Persistence failures the Store swallowed (in-memory state wins). `op`
	// is a low-cardinality label naming the failed operation (e.g.
	// resign_persist) so alerts can fire without exploding cardinality.
	persistErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gemline_persist_errors_total",
		Help: "Persistence errors swallowed by the Store (in-memory state wins).",
	}, []string{"op"})
)

func metricsHandler() http.Handler { return promhttp.Handler() }

// metricsMiddleware records request count + latency. /ws/* is skipped: the
// upgrade keeps the handler alive for the connection lifetime, so its duration
// is meaningless and live counts are already in wsConnections.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		status := strconv.Itoa(rec.status)
		httpRequestsTotal.WithLabelValues(r.Method, route, status).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route, status).Observe(time.Since(start).Seconds())
	})
}

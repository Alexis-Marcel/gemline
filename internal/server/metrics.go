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

// Cardinality stays bounded because `route` is the http.ServeMux
// *pattern* (e.g. `GET /api/games/{id}`), not the concrete path — so
// 10k distinct game IDs collapse to a single label value.
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
)

// metricsHandler serves the Prometheus exposition format on /metrics.
func metricsHandler() http.Handler { return promhttp.Handler() }

// metricsMiddleware records request count + latency for every routed
// request. /ws/* is skipped because the upgrade keeps the handler
// running for the connection lifetime — its duration is meaningless as
// a request-latency observation, and active connection counts are
// already tracked by wsConnections.
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

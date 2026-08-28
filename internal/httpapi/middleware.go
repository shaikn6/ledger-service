package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type ctxKey int

const requestIDKey ctxKey = 0

var (
	reqTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_http_requests_total",
		Help: "Total HTTP requests by method, route and status.",
	}, []string{"method", "route", "status"})

	reqDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ledger_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
)

// requestID attaches a request ID (from the inbound header or freshly minted)
// to the context and the response.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// recoverer turns a panic in a handler into a 500 and a logged error rather
// than a dropped connection.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"panic", rec,
						"path", r.URL.Path,
						"request_id", RequestIDFromContext(r.Context()))
					writeError(w, http.StatusInternalServerError, "internal", "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// observability logs one structured line per request and records Prometheus
// metrics. It relies on chi's route pattern for low-cardinality labels.
func observability(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			route := routePattern(r)
			elapsed := time.Since(start)
			reqTotal.WithLabelValues(r.Method, route, http.StatusText(sw.status)).Inc()
			reqDuration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())

			log.Info("request",
				"method", r.Method,
				"route", route,
				"path", r.URL.Path,
				"status", sw.status,
				"bytes", sw.written,
				"latency_ms", float64(elapsed.Microseconds())/1000.0,
				"remote", clientIP(r),
				"request_id", RequestIDFromContext(r.Context()))
		})
	}
}

// RequestIDFromContext returns the request ID for r's context, or "".
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	written int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.written += n
	return n, err
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	return r.RemoteAddr
}

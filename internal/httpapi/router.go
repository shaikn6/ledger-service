// Package httpapi exposes the ledger service over a JSON/HTTP API.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRouter wires the full middleware stack and routes for the service.
func NewRouter(h *Handlers, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(recoverer(log))
	r.Use(observability(log))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", h.Readyz)
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())

	r.Route("/v1", func(r chi.Router) {
		r.Post("/accounts", h.CreateAccount)
		r.Get("/accounts/{id}", h.GetAccount)
		r.Get("/accounts/{id}/postings", h.ListPostings)
		r.Post("/transfers", h.CreateTransfer)
		r.Get("/transfers/{id}", h.GetTransfer)
	})

	return r
}

func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
		return rctx.RoutePattern()
	}
	return "unmatched"
}

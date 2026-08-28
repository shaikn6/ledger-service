// Package httpapi exposes the ledger service over a JSON/HTTP API.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/shaikn6/ledger-service/api"
)

// Options configures the router.
type Options struct {
	Logger *slog.Logger
	// AuthTokens, when non-empty, requires a matching `Authorization: Bearer`
	// header on every /v1 route. Empty means the /v1 surface is open.
	AuthTokens []string
}

// NewRouter wires the middleware stack and routes for the service.
func NewRouter(h *Handlers, opt Options) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(recoverer(opt.Logger))
	r.Use(observability(opt.Logger))

	// Unauthenticated operational endpoints.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", h.Readyz)
	r.Get("/version", h.Version)
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(api.OpenAPISpec)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(bearerAuth(opt.AuthTokens))

		r.Get("/accounts", h.ListAccounts)
		r.Post("/accounts", h.CreateAccount)
		r.Get("/accounts/{id}", h.GetAccount)
		r.Get("/accounts/{id}/postings", h.ListPostings)

		r.Get("/transfers", h.ListTransfers)
		r.Post("/transfers", h.CreateTransfer)
		r.Get("/transfers/{id}", h.GetTransfer)
		r.Get("/transfers/{id}/postings", h.TransferPostings)
		r.Post("/transfers/{id}/reversals", h.ReverseTransfer)
	})

	return r
}

func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePattern() != "" {
		return rctx.RoutePattern()
	}
	return "unmatched"
}

# Changelog

## [0.1.0] - 2026-08-27

### Added
- Double-entry ledger over Postgres: accounts, idempotent transfers, append-only
  postings with running `balance_after`
- `POST /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/postings`
  (keyset pagination), `POST /v1/transfers` (requires `Idempotency-Key`),
  `GET /v1/transfers/{id}`
- Ordered `SELECT ... FOR UPDATE` row locking with bounded retry on
  serialization failures / deadlocks
- Currency-match and sufficient-funds enforcement; optional per-account overdraft
- Embedded SQL migrations applied on startup
- `slog` JSON logging, request-ID propagation, panic recovery, Prometheus
  metrics at `/metrics`, `/healthz` and DB-checking `/readyz`
- Graceful shutdown, per-request timeout, 1 MiB body cap, strict JSON decoding
- Multi-stage distroless Dockerfile, docker-compose, GitHub Actions CI
  (build + vet + race tests against a Postgres service + lint + docker build)

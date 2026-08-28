# Changelog

All notable changes to this project are documented here. This project adheres
to [Semantic Versioning](https://semver.org).

## [Unreleased]

### Changed
- Go 1.27.0 toolchain; `github.com/go-chi/chi/v5` v5.3.2,
  `prometheus/client_golang` v1.24.1; `golang:1.27.0-alpine` build image;
  CI actions bumped (`checkout@v7`, `setup-go@v7`, `golangci-lint-action@v9`).

### Security
- Bump `golang.org/x/text` to v0.41.0 (GO-2026-5970 — reachable via pgx SCRAM
  auth). Combined with the Go 1.27 toolchain, `govulncheck` reports no
  vulnerabilities.
- CI `docker` job now boots the built image against a Postgres service and
  checks `/readyz` + `/version`.

## [0.2.0] - 2026-08-28

### Added
- **Transfer reversals** — `POST /v1/transfers/{id}/reversals` posts a
  compensating transfer, marks the original `reversed`, and links the pair. A
  transfer can be reversed at most once (enforced by a partial unique index); a
  reversal cannot itself be reversed. Idempotent on `Idempotency-Key`.
- **List endpoints** — `GET /v1/accounts` and `GET /v1/transfers` (the latter
  with an optional `account_id` filter), keyset-paginated on `(created_at, id)`
  with an opaque `next_cursor`.
- **`GET /v1/transfers/{id}/postings`** — the two postings for a transfer.
- **`GET /version`** — build metadata (`version`, `commit`, `date`) injected via
  `-ldflags`.
- **Optional bearer-token auth** — `LEDGER_API_TOKENS` (comma-separated); when
  set, all `/v1` routes require `Authorization: Bearer <token>`, compared in
  constant time. Operational endpoints stay open.
- **OpenAPI 3.1 spec** at `api/openapi.yaml`, served from `GET /openapi.yaml`.
- **Business metrics** — `ledger_transfers_total{result}`,
  `ledger_transfer_amount_minor_units` histogram, `ledger_accounts_created_total`.
- **Configurable pool** — `DB_MAX_CONNS`, `DB_MIN_CONNS`.
- Repo hygiene: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, issue/PR templates,
  `CODEOWNERS`, Dependabot (gomod + actions + docker), `.dockerignore`.
- CI: `govulncheck` job; image build now stamps version info and smoke-tests
  the container.

### Changed
- Migration `0002_reversals.sql` adds `transfers.reverses_transfer_id` and a
  `status` check constraint (`posted` | `reversed`).

## [0.1.0] - 2026-08-27

### Added
- Double-entry ledger over Postgres: accounts, idempotent transfers,
  append-only postings with running `balance_after`.
- `POST /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/postings`,
  `POST /v1/transfers` (requires `Idempotency-Key`), `GET /v1/transfers/{id}`.
- Ordered `SELECT ... FOR UPDATE` row locking with bounded retry on
  serialization failures / deadlocks.
- Currency-match and sufficient-funds enforcement; optional per-account overdraft.
- Embedded SQL migrations applied on startup.
- `slog` JSON logging, request-ID propagation, panic recovery, Prometheus
  metrics, `/healthz` and DB-checking `/readyz`.
- Graceful shutdown, per-request timeout, 1 MiB body cap, strict JSON decoding.
- Multi-stage distroless Dockerfile, docker-compose, GitHub Actions CI.

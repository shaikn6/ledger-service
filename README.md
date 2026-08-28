# ledger-service

[![CI](https://github.com/shaikn6/ledger-service/actions/workflows/ci.yml/badge.svg)](https://github.com/shaikn6/ledger-service/actions)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/shaikn6/ledger-service)](https://goreportcard.com/report/github.com/shaikn6/ledger-service)
[![OpenAPI 3.1](https://img.shields.io/badge/OpenAPI-3.1-6BA539?logo=openapiinitiative&logoColor=white)](api/openapi.yaml)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**A double-entry accounting ledger as a Go microservice.** Every movement of
money is an idempotent `Transfer` that produces exactly two balanced
`Posting`s — one debit, one credit — applied atomically with both account
balances inside a single Postgres transaction. Transfers can be reversed
exactly once with a compensating transfer.

This is the kind of service that sits under a payments or wallet product: the
part that has to be *correct* under concurrency, not just fast.

## Why it's built this way

| Concern | Approach |
|---------|----------|
| **Money** | `int64` minor units and an ISO-4217 currency string. No floating point, anywhere. |
| **Balanced books** | Two postings per transfer, equal and opposite. `postings` is append-only. `accounts.balance` is a cached running total mutated only in the same transaction as its postings, so it can never drift from `SUM(postings)`. |
| **Concurrency** | Both account rows are taken with `SELECT ... FOR UPDATE` in a deterministic order (lowest UUID first). Concurrent transfers over the same pair serialize on the row locks and cannot deadlock. Serialization failures (`40001`) and deadlocks (`40P01`) are retried with backoff. |
| **Idempotency** | `transfers.idempotency_key` is `UNIQUE`. A retried request with the same key and body returns the original transfer; the same key with a *different* body is a `409`. The unique-violation race is handled. |
| **Reversals** | A reversal is a normal transfer in the opposite direction, tagged `reverses_transfer_id`. A partial unique index guarantees at most one reversal per original, even under concurrency; the original flips to `status = 'reversed'` in the same transaction. |
| **Invariants at the edge** | DB `CHECK` constraints (`amount > 0`, `balance >= 0` unless overdraft, distinct debit/credit accounts) back up the application checks. |
| **Migrations** | Plain `.sql` files embedded with `go:embed`, applied on startup, tracked in `schema_migrations`. |

## API

Full contract: [`api/openapi.yaml`](api/openapi.yaml), also served live at
`GET /openapi.yaml`.

| Method | Path | Notes |
|--------|------|-------|
| `POST` | `/v1/accounts` | `{name, currency, allow_overdraft?}` → `201` |
| `GET` | `/v1/accounts` | list, newest-first, `?limit=&cursor=` |
| `GET` | `/v1/accounts/{id}` | account with current balance |
| `GET` | `/v1/accounts/{id}/postings` | `?limit=&before=` — keyset on posting id |
| `POST` | `/v1/transfers` | requires `Idempotency-Key` header |
| `GET` | `/v1/transfers` | list, `?account_id=&limit=&cursor=` |
| `GET` | `/v1/transfers/{id}` | transfer by id |
| `GET` | `/v1/transfers/{id}/postings` | the two postings for the transfer |
| `POST` | `/v1/transfers/{id}/reversals` | requires `Idempotency-Key`; posts a compensating transfer |
| `GET` | `/healthz` · `/readyz` · `/version` | liveness · readiness (pings DB) · build info |
| `GET` | `/metrics` | Prometheus (HTTP + business metrics) |

Errors are always `{"error": {"code": "...", "message": "..."}}` with a stable
`code`. List responses are `{"data": [...], "next_cursor": "..."}` (`next_cursor`
is empty at the end). Request bodies are capped at 1 MiB and unknown JSON
fields are rejected.

### Example

```bash
SRC=$(curl -s localhost:8080/v1/accounts -d '{"name":"treasury","currency":"USD","allow_overdraft":true}' | jq -r .id)
DST=$(curl -s localhost:8080/v1/accounts -d '{"name":"merchant","currency":"USD"}' | jq -r .id)

# move $25.00 — retrying this exact call is safe, funds move once
TR=$(curl -s localhost:8080/v1/transfers -H "Idempotency-Key: order-1001" \
  -d "{\"debit_account_id\":\"$SRC\",\"credit_account_id\":\"$DST\",\"amount\":2500,\"currency\":\"USD\"}" | jq -r .id)

curl -s localhost:8080/v1/accounts/$DST | jq .balance          # 2500

# refund it — balances return to 0, original becomes "reversed"
curl -s -X POST localhost:8080/v1/transfers/$TR/reversals -H "Idempotency-Key: refund-1001"
curl -s localhost:8080/v1/accounts/$DST | jq .balance          # 0
```

## Run it

```bash
docker compose up --build        # ledger on :8080, Postgres on :5432
```

Or locally against your own Postgres:

```bash
export DATABASE_URL='postgres://ledger:ledger@localhost:5432/ledger?sslmode=disable'
make run
```

### Configuration (environment)

| Var | Default | |
|-----|---------|--|
| `DATABASE_URL` | — | **required** |
| `LEDGER_ADDR` | `:8080` | listen address |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `SHUTDOWN_TIMEOUT` | `15s` | graceful-shutdown budget |
| `REQUEST_TIMEOUT` | `10s` | per-request deadline |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | `10` / `0` | pool sizing |
| `LEDGER_API_TOKENS` | — | comma-separated bearer tokens; when set, `/v1/*` requires `Authorization: Bearer <token>` |

Operational endpoints (`/healthz`, `/readyz`, `/version`, `/metrics`,
`/openapi.yaml`) are always unauthenticated. See [SECURITY.md](SECURITY.md).

## Tests

```bash
make dev-db        # throwaway Postgres on :55432
make test          # unit + Postgres-backed integration, with -race
make dev-db-stop
```

`internal/money` and `internal/config` are pure unit tests. `internal/httpapi`
tests the HTTP layer against an in-memory fake (routing, error mapping,
idempotency headers, bearer auth, metrics, OpenAPI serving). `internal/ledger`
runs against a real Postgres (`TEST_DATABASE_URL`, skipped when unset) and
covers transfers and balance math, insufficient funds, currency mismatch,
idempotent replay, idempotency conflict, reversals (restore balances, replay,
already-reversed, reverse-a-reversal), keyset pagination, and a **40-goroutine
concurrent bidirectional transfer test that asserts total balance is conserved
and never goes negative**. CI runs the full suite with `-race` against a
Postgres service container, plus `golangci-lint`, `govulncheck`, and a
container smoke test on every push.

## Layout

```
api/                   OpenAPI 3.1 spec (embedded + served)
cmd/ledger/            entrypoint: config, pool, migrate, serve, graceful shutdown
internal/config/       env configuration
internal/money/        integer-minor-unit money type + parsing
internal/store/        pgx pool + embedded SQL migrator
internal/ledger/       the domain: accounts, transfers, reversals, postings, pagination
internal/httpapi/      chi router, handlers, middleware (request id, recover, auth, metrics)
```

## License

MIT

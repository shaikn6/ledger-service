# ledger-service

[![CI](https://github.com/shaikn6/ledger-service/actions/workflows/ci.yml/badge.svg)](https://github.com/shaikn6/ledger-service/actions)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**A double-entry accounting ledger as a Go microservice.** Every movement of
money is an idempotent `Transfer` that produces exactly two balanced
`Posting`s — one debit, one credit — applied atomically with both account
balances inside a single Postgres transaction.

This is the kind of service that sits under a payments or wallet product: the
part that has to be *correct* under concurrency, not just fast.

## Why it's built this way

| Concern | Approach |
|---------|----------|
| **Money** | `int64` minor units and an ISO-4217 currency string. No floating point, anywhere. |
| **Balanced books** | Two postings per transfer, equal and opposite. `postings` is append-only — no `UPDATE`, no `DELETE`. `accounts.balance` is a cached running total mutated only in the same transaction as its postings, so it can never drift from `SUM(postings)`. |
| **Concurrency** | Both account rows are taken with `SELECT ... FOR UPDATE` in a deterministic order (lowest UUID first). Concurrent transfers over the same pair serialize on the row locks and cannot deadlock. Serialization failures (`40001`) and deadlocks (`40P01`) are retried with backoff as defense-in-depth. |
| **Idempotency** | `transfers.idempotency_key` is `UNIQUE`. A retried request with the same key and an identical body returns the original transfer; the same key with a *different* body is a `409 idempotency_conflict`. The unique-violation race is handled — a concurrent duplicate resolves to the winner's result. |
| **Invariants at the edge** | DB `CHECK` constraints (`amount > 0`, `balance >= 0` unless overdraft, distinct debit/credit accounts) back up the application checks. |
| **Migrations** | Plain `.sql` files embedded with `go:embed`, applied on startup, tracked in `schema_migrations`. No external migration tool. |

## API

| Method | Path | Notes |
|--------|------|-------|
| `POST` | `/v1/accounts` | `{name, currency, allow_overdraft?}` → `201` account |
| `GET` | `/v1/accounts/{id}` | account with current balance |
| `GET` | `/v1/accounts/{id}/postings?limit=&before=` | newest-first, keyset paginated on posting id |
| `POST` | `/v1/transfers` | requires `Idempotency-Key` header; `{debit_account_id, credit_account_id, amount, currency}` → `201` transfer |
| `GET` | `/v1/transfers/{id}` | transfer by id |
| `GET` | `/healthz` · `/readyz` | liveness · readiness (pings the DB) |
| `GET` | `/metrics` | Prometheus |

Errors are always `{"error": {"code": "...", "message": "..."}}` with a stable
`code`. Request bodies are capped at 1 MiB and unknown JSON fields are rejected.

### Example

```bash
# open two accounts
SRC=$(curl -s localhost:8080/v1/accounts -d '{"name":"treasury","currency":"USD","allow_overdraft":true}' | jq -r .id)
DST=$(curl -s localhost:8080/v1/accounts -d '{"name":"merchant","currency":"USD"}' | jq -r .id)

# move $25.00 — retrying this exact call is safe, funds move once
curl -s localhost:8080/v1/transfers \
  -H "Idempotency-Key: order-1001" \
  -d "{\"debit_account_id\":\"$SRC\",\"credit_account_id\":\"$DST\",\"amount\":2500,\"currency\":\"USD\"}"

curl -s localhost:8080/v1/accounts/$DST      # balance: 2500
curl -s localhost:8080/v1/accounts/$DST/postings
```

## Run it

```bash
docker compose up --build        # ledger on :8080, Postgres on :5432
```

Or locally against your own Postgres:

```bash
export DATABASE_URL='postgres://ledger:ledger@localhost:5432/ledger?sslmode=disable'
go run ./cmd/ledger
```

Configuration (env): `DATABASE_URL` (required), `LEDGER_ADDR` (`:8080`),
`LOG_LEVEL` (`info`), `SHUTDOWN_TIMEOUT` (`15s`), `REQUEST_TIMEOUT` (`10s`).

## Tests

```bash
make dev-db        # throwaway Postgres on :55432
make test          # unit + Postgres-backed integration, with -race
make dev-db-stop
```

`internal/money` and `internal/config` are pure unit tests. `internal/httpapi`
tests the HTTP layer against an in-memory fake (routing, error mapping,
idempotency-header handling, metrics). `internal/ledger` runs against a real
Postgres (`TEST_DATABASE_URL`, skipped when unset) and covers successful
transfers and balance math, insufficient funds, currency mismatch, idempotent
replay, idempotency conflict, and a **40-goroutine concurrent bidirectional
transfer test that asserts total balance is conserved and never goes negative**.
CI runs the full suite with `-race` against a Postgres service container on
every push.

## Layout

```
cmd/ledger/            entrypoint: config, pool, migrate, serve, graceful shutdown
internal/config/       env configuration
internal/money/        integer-minor-unit money type + parsing
internal/store/        pgx pool + embedded SQL migrator
internal/ledger/       the domain: accounts, transfers, postings, invariants
internal/httpapi/      chi router, handlers, middleware (request id, recover, metrics)
```

## License

MIT

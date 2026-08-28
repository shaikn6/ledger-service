# Contributing

Thanks for taking the time to contribute.

## Development setup

```bash
make dev-db          # throwaway Postgres on :55432 (needs Docker)
make test            # unit + integration, with -race
make lint            # golangci-lint
make dev-db-stop
```

`TEST_DATABASE_URL` points the integration tests at Postgres. Tests that need
it are skipped when it is unset, so `go test ./...` always works — but CI runs
the full suite and so should you before opening a PR.

## Ground rules

- **One logical change per PR.** Keep diffs reviewable.
- **Tests are not optional.** New behaviour needs a test; bug fixes need a test
  that fails before the fix.
- **The build must be green:** `go build ./...`, `go vet ./...`,
  `golangci-lint run`, and `make test` all pass.
- **Money is integer minor units.** Never introduce a float into a balance,
  amount, or posting.
- **Migrations are append-only.** Add a new `internal/store/migrations/NNNN_*.sql`
  file; never edit one that has shipped.
- **Keep the OpenAPI spec in sync.** If you change a route or payload, update
  `api/openapi.yaml` in the same PR.

## Commit messages

Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).

## Reporting bugs / proposing features

Use the issue templates. For anything security-related, follow
[SECURITY.md](SECURITY.md) instead of opening a public issue.

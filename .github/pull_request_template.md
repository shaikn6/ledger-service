## What

<!-- One or two sentences on the change. -->

## Why

<!-- The problem this solves or the behaviour it adds. -->

## Checklist

- [ ] `go build ./...`, `go vet ./...`, `golangci-lint run` pass
- [ ] `make test` passes (unit + integration, with `-race`)
- [ ] New behaviour has tests; bug fixes have a regression test
- [ ] `api/openapi.yaml` updated if a route or payload changed
- [ ] New migration file added (not an edit to a shipped one) if the schema changed
- [ ] `CHANGELOG.md` updated

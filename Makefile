TEST_DATABASE_URL ?= postgres://postgres:pg@localhost:55432/ledger?sslmode=disable
VERSION           ?= dev
COMMIT            := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE              := $(shell date -u +%FT%TZ)
LDFLAGS           := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test test-unit lint vet vuln cover run dev-db dev-db-stop tidy openapi-preview

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ledger ./cmd/ledger

## test: run everything, including Postgres-backed integration tests
test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./... -count=1 -race

## test-unit: run only tests that need no database
test-unit:
	go test ./internal/money/... ./internal/config/... ./internal/httpapi/... -count=1 -race

## cover: write and summarise a coverage profile
cover:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

lint:
	golangci-lint run ./...

## vuln: check dependencies against the Go vulnerability database
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## dev-db: start a throwaway Postgres for local integration tests
dev-db:
	docker run -d --rm --name ledger-dev-pg -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=ledger -p 55432:5432 postgres:16-alpine

dev-db-stop:
	docker stop ledger-dev-pg

run:
	DATABASE_URL="$(TEST_DATABASE_URL)" go run -ldflags "$(LDFLAGS)" ./cmd/ledger

tidy:
	go mod tidy

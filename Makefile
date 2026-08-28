TEST_DATABASE_URL ?= postgres://postgres:pg@localhost:55432/ledger?sslmode=disable

.PHONY: build test test-unit lint vet run dev-db dev-db-stop tidy

build:
	go build -trimpath -o bin/ledger ./cmd/ledger

## test: run everything, including Postgres-backed integration tests
test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test ./... -count=1 -race

## test-unit: run only tests that need no database
test-unit:
	go test ./internal/money/... ./internal/config/... ./internal/httpapi/... -count=1 -race

vet:
	go vet ./...

lint:
	golangci-lint run ./...

## dev-db: start a throwaway Postgres for local integration tests
dev-db:
	docker run -d --rm --name ledger-dev-pg -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=ledger -p 55432:5432 postgres:16-alpine

dev-db-stop:
	docker stop ledger-dev-pg

run:
	DATABASE_URL="$(TEST_DATABASE_URL)" go run ./cmd/ledger

tidy:
	go mod tidy

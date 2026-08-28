package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/shaikn6/ledger-service/internal/httpapi"
	"github.com/shaikn6/ledger-service/internal/ledger"
)

// fakeLedger is an in-memory Ledger for testing the HTTP layer in isolation.
type fakeLedger struct {
	accounts    map[uuid.UUID]ledger.Account
	transfers   map[uuid.UUID]ledger.Transfer
	pingErr     error
	transferErr error
	reverseErr  error
}

func newFake() *fakeLedger {
	return &fakeLedger{
		accounts:  map[uuid.UUID]ledger.Account{},
		transfers: map[uuid.UUID]ledger.Transfer{},
	}
}

func (f *fakeLedger) CreateAccount(_ context.Context, p ledger.NewAccountParams) (ledger.Account, error) {
	if p.Name == "" || len(p.Currency) != 3 {
		return ledger.Account{}, ledger.ErrValidation
	}
	a := ledger.Account{ID: uuid.New(), Name: p.Name, Currency: p.Currency, AllowOverdraft: p.AllowOverdraft}
	f.accounts[a.ID] = a
	return a, nil
}

func (f *fakeLedger) GetAccount(_ context.Context, id uuid.UUID) (ledger.Account, error) {
	a, ok := f.accounts[id]
	if !ok {
		return ledger.Account{}, ledger.ErrAccountNotFound
	}
	return a, nil
}

func (f *fakeLedger) ListAccounts(context.Context, int, string) (ledger.Page[ledger.Account], error) {
	out := make([]ledger.Account, 0, len(f.accounts))
	for _, a := range f.accounts {
		out = append(out, a)
	}
	return ledger.Page[ledger.Account]{Data: out}, nil
}

func (f *fakeLedger) CreateTransfer(_ context.Context, p ledger.NewTransferParams) (ledger.Transfer, error) {
	if f.transferErr != nil {
		return ledger.Transfer{}, f.transferErr
	}
	t := ledger.Transfer{
		ID: uuid.New(), IdempotencyKey: p.IdempotencyKey,
		DebitAccountID: p.DebitAccountID, CreditAccountID: p.CreditAccountID,
		Amount: p.Amount, Currency: p.Currency, Status: "posted",
	}
	f.transfers[t.ID] = t
	return t, nil
}

func (f *fakeLedger) ReverseTransfer(_ context.Context, originalID uuid.UUID, key string) (ledger.Transfer, error) {
	if f.reverseErr != nil {
		return ledger.Transfer{}, f.reverseErr
	}
	t := ledger.Transfer{ID: uuid.New(), IdempotencyKey: key, Status: "posted", ReversesTransferID: &originalID}
	f.transfers[t.ID] = t
	return t, nil
}

func (f *fakeLedger) GetTransfer(_ context.Context, id uuid.UUID) (ledger.Transfer, error) {
	t, ok := f.transfers[id]
	if !ok {
		return ledger.Transfer{}, ledger.ErrTransferNotFound
	}
	return t, nil
}

func (f *fakeLedger) ListTransfers(context.Context, *uuid.UUID, int, string) (ledger.Page[ledger.Transfer], error) {
	out := make([]ledger.Transfer, 0, len(f.transfers))
	for _, t := range f.transfers {
		out = append(out, t)
	}
	return ledger.Page[ledger.Transfer]{Data: out}, nil
}

func (f *fakeLedger) ListPostings(context.Context, uuid.UUID, int, int64) ([]ledger.Posting, error) {
	return nil, nil
}

func (f *fakeLedger) TransferPostings(context.Context, uuid.UUID) ([]ledger.Posting, error) {
	return nil, nil
}

func (f *fakeLedger) Ping(context.Context) error { return f.pingErr }

func newTestServer(f *fakeLedger, tokens ...string) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.NewRouter(
		&httpapi.Handlers{Svc: f, Build: httpapi.BuildInfo{Version: "test"}},
		httpapi.Options{Logger: log, AuthTokens: tokens},
	)
}

func do(t *testing.T, srv http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHealthzAndVersion(t *testing.T) {
	srv := newTestServer(newFake())
	if rec := do(t, srv, http.MethodGet, "/healthz", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	rec := do(t, srv, http.MethodGet, "/version", nil, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"version":"test"`)) {
		t.Fatalf("version = %d body=%s", rec.Code, rec.Body)
	}
}

func TestReadyz(t *testing.T) {
	f := newFake()
	if rec := do(t, newTestServer(f), http.MethodGet, "/readyz", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("readyz healthy = %d", rec.Code)
	}
	f.pingErr = context.DeadlineExceeded
	if rec := do(t, newTestServer(f), http.MethodGet, "/readyz", nil, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz unhealthy = %d", rec.Code)
	}
}

func TestOpenAPISpecServed(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/openapi.yaml", nil, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("openapi: 3.1.0")) {
		t.Fatalf("openapi = %d body head=%.40s", rec.Code, rec.Body)
	}
}

func TestCreateAccount(t *testing.T) {
	srv := newTestServer(newFake())

	rec := do(t, srv, http.MethodPost, "/v1/accounts", map[string]any{"name": "ops", "currency": "USD"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body)
	}
	var acc ledger.Account
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil || acc.Currency != "USD" {
		t.Fatalf("bad body: %v %s", err, rec.Body)
	}

	if rec := do(t, srv, http.MethodPost, "/v1/accounts", map[string]any{"currency": "USD"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name should be 400, got %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodPost, "/v1/accounts", map[string]any{"name": "x", "currency": "USD", "bogus": 1}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field should be 400, got %d", rec.Code)
	}
}

func TestListEndpoints(t *testing.T) {
	f := newFake()
	f.accounts[uuid.New()] = ledger.Account{ID: uuid.New(), Currency: "USD"}
	srv := newTestServer(f)

	if rec := do(t, srv, http.MethodGet, "/v1/accounts", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list accounts = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/v1/transfers", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("list transfers = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/v1/transfers?account_id=not-a-uuid", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad account_id filter = %d", rec.Code)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	srv := newTestServer(newFake())
	if rec := do(t, srv, http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("= %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodGet, "/v1/accounts/not-a-uuid", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid = %d", rec.Code)
	}
}

func TestCreateTransfer(t *testing.T) {
	f := newFake()
	srv := newTestServer(f)
	payload := map[string]any{
		"debit_account_id": uuid.NewString(), "credit_account_id": uuid.NewString(),
		"amount": 500, "currency": "USD",
	}

	if rec := do(t, srv, http.MethodPost, "/v1/transfers", payload, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key should be 400, got %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "abc"}); rec.Code != http.StatusCreated {
		t.Fatalf("create transfer = %d body=%s", rec.Code, rec.Body)
	}

	f.transferErr = ledger.ErrInsufficientFunds
	if rec := do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "k2"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("insufficient funds = %d", rec.Code)
	}
	f.transferErr = ledger.ErrIdempotencyConflict
	if rec := do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "k3"}); rec.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict = %d", rec.Code)
	}
}

func TestReverseTransferHandler(t *testing.T) {
	f := newFake()
	srv := newTestServer(f)
	id := uuid.NewString()

	if rec := do(t, srv, http.MethodPost, "/v1/transfers/"+id+"/reversals", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d", rec.Code)
	}
	if rec := do(t, srv, http.MethodPost, "/v1/transfers/"+id+"/reversals", nil, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusCreated {
		t.Fatalf("reverse = %d body=%s", rec.Code, rec.Body)
	}

	f.reverseErr = ledger.ErrAlreadyReversed
	if rec := do(t, srv, http.MethodPost, "/v1/transfers/"+id+"/reversals", nil, map[string]string{"Idempotency-Key": "r2"}); rec.Code != http.StatusConflict {
		t.Fatalf("already reversed = %d", rec.Code)
	}
	f.reverseErr = ledger.ErrCannotReverseReversal
	if rec := do(t, srv, http.MethodPost, "/v1/transfers/"+id+"/reversals", nil, map[string]string{"Idempotency-Key": "r3"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cannot reverse reversal = %d", rec.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	f := newFake()
	srv := newTestServer(f, "secret-token")

	// Open endpoints stay open.
	if rec := do(t, srv, http.MethodGet, "/healthz", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz should be open, got %d", rec.Code)
	}
	// /v1 without a token -> 401.
	if rec := do(t, srv, http.MethodGet, "/v1/accounts", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}
	// Wrong token -> 401.
	if rec := do(t, srv, http.MethodGet, "/v1/accounts", nil, map[string]string{"Authorization": "Bearer nope"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", rec.Code)
	}
	// Correct token -> through.
	if rec := do(t, srv, http.MethodGet, "/v1/accounts", nil, map[string]string{"Authorization": "Bearer secret-token"}); rec.Code != http.StatusOK {
		t.Fatalf("valid token = %d, want 200", rec.Code)
	}
}

func TestErrorBodyShape(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, nil)
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body not JSON: %v", err)
	}
	if body.Error.Code != "account_not_found" || body.Error.Message == "" {
		t.Fatalf("unexpected error body: %s", rec.Body)
	}
}

func TestRequestIDEcho(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/healthz", nil, map[string]string{"X-Request-Id": "fixed-id"})
	if got := rec.Header().Get("X-Request-Id"); got != "fixed-id" {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

func TestMetricsExposed(t *testing.T) {
	srv := newTestServer(newFake())
	do(t, srv, http.MethodGet, "/healthz", nil, nil)
	rec := do(t, srv, http.MethodGet, "/metrics", nil, nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("ledger_http_requests_total")) {
		t.Fatalf("metrics = %d, body missing metric", rec.Code)
	}
}

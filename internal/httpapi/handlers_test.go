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

func (f *fakeLedger) GetTransfer(_ context.Context, id uuid.UUID) (ledger.Transfer, error) {
	t, ok := f.transfers[id]
	if !ok {
		return ledger.Transfer{}, ledger.ErrTransferNotFound
	}
	return t, nil
}

func (f *fakeLedger) ListPostings(context.Context, uuid.UUID, int, int64) ([]ledger.Posting, error) {
	return nil, nil
}

func (f *fakeLedger) Ping(context.Context) error { return f.pingErr }

func newTestServer(f *fakeLedger) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.NewRouter(&httpapi.Handlers{Svc: f}, log)
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

func TestHealthz(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
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

func TestCreateAccount(t *testing.T) {
	srv := newTestServer(newFake())

	rec := do(t, srv, http.MethodPost, "/v1/accounts",
		map[string]any{"name": "ops", "currency": "USD"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rec.Code, rec.Body)
	}
	var acc ledger.Account
	if err := json.Unmarshal(rec.Body.Bytes(), &acc); err != nil || acc.Currency != "USD" {
		t.Fatalf("bad body: %v %s", err, rec.Body)
	}

	rec = do(t, srv, http.MethodPost, "/v1/accounts", map[string]any{"currency": "USD"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name should be 400, got %d", rec.Code)
	}

	rec = do(t, srv, http.MethodPost, "/v1/accounts", map[string]any{"name": "x", "currency": "USD", "bogus": 1}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field should be 400, got %d", rec.Code)
	}
}

func TestGetAccountNotFound(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("= %d", rec.Code)
	}
	rec = do(t, newTestServer(newFake()), http.MethodGet, "/v1/accounts/not-a-uuid", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid = %d", rec.Code)
	}
}

func TestCreateTransfer(t *testing.T) {
	f := newFake()
	srv := newTestServer(f)
	debit, credit := uuid.NewString(), uuid.NewString()
	payload := map[string]any{
		"debit_account_id": debit, "credit_account_id": credit,
		"amount": 500, "currency": "USD",
	}

	rec := do(t, srv, http.MethodPost, "/v1/transfers", payload, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key should be 400, got %d", rec.Code)
	}

	rec = do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "abc"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create transfer = %d body=%s", rec.Code, rec.Body)
	}

	f.transferErr = ledger.ErrInsufficientFunds
	rec = do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "abc2"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("insufficient funds = %d", rec.Code)
	}

	f.transferErr = ledger.ErrIdempotencyConflict
	rec = do(t, srv, http.MethodPost, "/v1/transfers", payload, map[string]string{"Idempotency-Key": "abc3"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict = %d", rec.Code)
	}
}

func TestErrorBodyShape(t *testing.T) {
	rec := do(t, newTestServer(newFake()), http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, nil)
	var body struct {
		Error struct {
			Code, Message string
		}
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
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("ledger_http_requests_total")) {
		t.Fatalf("metric not present in output")
	}
}

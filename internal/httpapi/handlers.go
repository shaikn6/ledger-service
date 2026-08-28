package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/shaikn6/ledger-service/internal/ledger"
)

// Ledger is the behaviour Handlers needs from the ledger service.
type Ledger interface {
	CreateAccount(ctx context.Context, p ledger.NewAccountParams) (ledger.Account, error)
	GetAccount(ctx context.Context, id uuid.UUID) (ledger.Account, error)
	ListAccounts(ctx context.Context, limit int, cursor string) (ledger.Page[ledger.Account], error)
	CreateTransfer(ctx context.Context, p ledger.NewTransferParams) (ledger.Transfer, error)
	ReverseTransfer(ctx context.Context, originalID uuid.UUID, idempotencyKey string) (ledger.Transfer, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (ledger.Transfer, error)
	ListTransfers(ctx context.Context, accountID *uuid.UUID, limit int, cursor string) (ledger.Page[ledger.Transfer], error)
	ListPostings(ctx context.Context, accountID uuid.UUID, limit int, beforeID int64) ([]ledger.Posting, error)
	TransferPostings(ctx context.Context, transferID uuid.UUID) ([]ledger.Posting, error)
	Ping(ctx context.Context) error
}

// BuildInfo is served by GET /version.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Handlers holds the dependencies for the HTTP handlers.
type Handlers struct {
	Svc   Ledger
	Build BuildInfo
}

// Version handles GET /version.
func (h *Handlers) Version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.Build)
}

// Readyz reports 200 only when the database is reachable.
func (h *Handlers) Readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.Svc.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type createAccountRequest struct {
	Name           string `json:"name"`
	Currency       string `json:"currency"`
	AllowOverdraft bool   `json:"allow_overdraft"`
}

// CreateAccount handles POST /v1/accounts.
func (h *Handlers) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req createAccountRequest
	if !decode(w, r, &req) {
		return
	}
	acc, err := h.Svc.CreateAccount(r.Context(), ledger.NewAccountParams{
		Name:           req.Name,
		Currency:       req.Currency,
		AllowOverdraft: req.AllowOverdraft,
	})
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	metricAccountsCreated.Inc()
	writeJSON(w, http.StatusCreated, acc)
}

// GetAccount handles GET /v1/accounts/{id}.
func (h *Handlers) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	acc, err := h.Svc.GetAccount(r.Context(), id)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// ListAccounts handles GET /v1/accounts?limit=&cursor=.
func (h *Handlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	limit, cursor := pageParams(r)
	page, err := h.Svc.ListAccounts(r.Context(), limit, cursor)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

type createTransferRequest struct {
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	Amount          int64  `json:"amount"`
	Currency        string `json:"currency"`
}

// CreateTransfer handles POST /v1/transfers. The Idempotency-Key header is
// required; replaying it returns the original transfer.
func (h *Handlers) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required")
		return
	}
	var req createTransferRequest
	if !decode(w, r, &req) {
		return
	}
	debit, err1 := uuid.Parse(req.DebitAccountID)
	credit, err2 := uuid.Parse(req.CreditAccountID)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "debit_account_id and credit_account_id must be UUIDs")
		return
	}

	t, err := h.Svc.CreateTransfer(r.Context(), ledger.NewTransferParams{
		IdempotencyKey:  key,
		DebitAccountID:  debit,
		CreditAccountID: credit,
		Amount:          req.Amount,
		Currency:        req.Currency,
	})
	if err != nil {
		metricTransfers.WithLabelValues("rejected").Inc()
		writeLedgerError(w, err)
		return
	}
	recordTransfer(t)
	writeJSON(w, http.StatusCreated, t)
}

// ReverseTransfer handles POST /v1/transfers/{id}/reversals.
func (h *Handlers) ReverseTransfer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required")
		return
	}
	t, err := h.Svc.ReverseTransfer(r.Context(), id, key)
	if err != nil {
		metricTransfers.WithLabelValues("rejected").Inc()
		writeLedgerError(w, err)
		return
	}
	recordTransfer(t)
	writeJSON(w, http.StatusCreated, t)
}

// GetTransfer handles GET /v1/transfers/{id}.
func (h *Handlers) GetTransfer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	t, err := h.Svc.GetTransfer(r.Context(), id)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ListTransfers handles GET /v1/transfers?account_id=&limit=&cursor=.
func (h *Handlers) ListTransfers(w http.ResponseWriter, r *http.Request) {
	limit, cursor := pageParams(r)

	var accountID *uuid.UUID
	if raw := r.URL.Query().Get("account_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_account_id", "account_id must be a UUID")
			return
		}
		accountID = &id
	}

	page, err := h.Svc.ListTransfers(r.Context(), accountID, limit, cursor)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// TransferPostings handles GET /v1/transfers/{id}/postings.
func (h *Handlers) TransferPostings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	postings, err := h.Svc.TransferPostings(r.Context(), id)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"postings": postings})
}

// ListPostings handles GET /v1/accounts/{id}/postings?limit=&before=.
func (h *Handlers) ListPostings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)

	postings, err := h.Svc.ListPostings(r.Context(), id, limit, before)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"postings": postings})
}

// --- helpers ---

func pageParams(r *http.Request) (int, string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return limit, r.URL.Query().Get("cursor")
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

func pathUUID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "path id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}

func writeLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrAccountNotFound):
		writeError(w, http.StatusNotFound, "account_not_found", err.Error())
	case errors.Is(err, ledger.ErrTransferNotFound):
		writeError(w, http.StatusNotFound, "transfer_not_found", err.Error())
	case errors.Is(err, ledger.ErrCurrencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "currency_mismatch", err.Error())
	case errors.Is(err, ledger.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", err.Error())
	case errors.Is(err, ledger.ErrAlreadyReversed):
		writeError(w, http.StatusConflict, "already_reversed", err.Error())
	case errors.Is(err, ledger.ErrCannotReverseReversal):
		writeError(w, http.StatusUnprocessableEntity, "cannot_reverse_reversal", err.Error())
	case errors.Is(err, ledger.ErrSameAccount):
		writeError(w, http.StatusBadRequest, "same_account", err.Error())
	case errors.Is(err, ledger.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.Is(err, ledger.ErrInvalidCursor):
		writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
	case errors.Is(err, ledger.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

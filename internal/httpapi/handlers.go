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
	CreateTransfer(ctx context.Context, p ledger.NewTransferParams) (ledger.Transfer, error)
	GetTransfer(ctx context.Context, id uuid.UUID) (ledger.Transfer, error)
	ListPostings(ctx context.Context, accountID uuid.UUID, limit int, beforeID int64) ([]ledger.Posting, error)
	Ping(ctx context.Context) error
}

// Handlers holds the dependencies for the HTTP handlers.
type Handlers struct {
	Svc Ledger
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
		writeLedgerError(w, err)
		return
	}
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
	raw := chi.URLParam(r, "id")
	id, err := uuid.Parse(raw)
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
	case errors.Is(err, ledger.ErrSameAccount):
		writeError(w, http.StatusBadRequest, "same_account", err.Error())
	case errors.Is(err, ledger.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
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

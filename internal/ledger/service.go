// Package ledger implements double-entry accounting over Postgres.
//
// Every money movement is a Transfer that produces exactly two Postings — one
// debit, one credit, equal in magnitude — applied atomically with the running
// balances of both accounts. Transfers are idempotent on a caller-supplied key.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shaikn6/ledger-service/internal/store"
)

// Account is a balance held in a single currency.
type Account struct {
	ID             uuid.UUID `json:"id"`
	Name           string    `json:"name"`
	Currency       string    `json:"currency"`
	Balance        int64     `json:"balance"`
	AllowOverdraft bool      `json:"allow_overdraft"`
	CreatedAt      time.Time `json:"created_at"`
}

// Transfer is a completed movement of funds between two accounts.
type Transfer struct {
	ID              uuid.UUID `json:"id"`
	IdempotencyKey  string    `json:"idempotency_key"`
	DebitAccountID  uuid.UUID `json:"debit_account_id"`
	CreditAccountID uuid.UUID `json:"credit_account_id"`
	Amount          int64     `json:"amount"`
	Currency        string    `json:"currency"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// Posting is one side of a transfer as recorded against an account.
type Posting struct {
	ID           int64     `json:"id"`
	TransferID   uuid.UUID `json:"transfer_id"`
	AccountID    uuid.UUID `json:"account_id"`
	Direction    string    `json:"direction"`
	Amount       int64     `json:"amount"`
	BalanceAfter int64     `json:"balance_after"`
	CreatedAt    time.Time `json:"created_at"`
}

// Service is the ledger's public API. It is safe for concurrent use.
type Service struct {
	pool store.Pool
}

// New returns a Service backed by the given pool.
func New(pool store.Pool) *Service { return &Service{pool: pool} }

// NewAccountParams are the inputs to CreateAccount.
type NewAccountParams struct {
	Name           string
	Currency       string
	AllowOverdraft bool
}

// CreateAccount opens a new account with a zero balance.
func (s *Service) CreateAccount(ctx context.Context, p NewAccountParams) (Account, error) {
	if p.Name == "" {
		return Account{}, fmt.Errorf("%w: name is required", ErrValidation)
	}
	if !validCurrency(p.Currency) {
		return Account{}, fmt.Errorf("%w: currency must be a 3-letter ISO 4217 code", ErrValidation)
	}
	a := Account{
		ID:             uuid.New(),
		Name:           p.Name,
		Currency:       p.Currency,
		AllowOverdraft: p.AllowOverdraft,
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, name, currency, allow_overdraft)
		VALUES ($1, $2, $3, $4)
		RETURNING balance, created_at`,
		a.ID, a.Name, a.Currency, a.AllowOverdraft,
	).Scan(&a.Balance, &a.CreatedAt)
	if err != nil {
		return Account{}, fmt.Errorf("insert account: %w", err)
	}
	return a, nil
}

// GetAccount returns an account by ID.
func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	return scanAccount(s.pool.QueryRow(ctx, accountByIDSQL, id))
}

// NewTransferParams are the inputs to CreateTransfer.
type NewTransferParams struct {
	IdempotencyKey  string
	DebitAccountID  uuid.UUID
	CreditAccountID uuid.UUID
	Amount          int64
	Currency        string
}

// CreateTransfer moves Amount minor units from the debit account to the credit
// account. It is idempotent: calling it again with the same IdempotencyKey and
// an identical request returns the original transfer without moving funds
// again; the same key with a different request is an ErrIdempotencyConflict.
//
// The whole operation runs in one transaction. Both account rows are locked
// with SELECT ... FOR UPDATE in a deterministic order (lowest UUID first), so
// concurrent transfers between the same pair serialize on the row locks and
// cannot deadlock. Deadlocks (SQLSTATE 40P01) and serialization failures
// (40001) are still retried a bounded number of times with backoff as
// defense-in-depth.
func (s *Service) CreateTransfer(ctx context.Context, p NewTransferParams) (Transfer, error) {
	if err := validateTransfer(p); err != nil {
		return Transfer{}, err
	}

	if existing, err := s.transferByKey(ctx, p.IdempotencyKey); err == nil {
		if !sameRequest(existing, p) {
			return Transfer{}, ErrIdempotencyConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrTransferNotFound) {
		return Transfer{}, err
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Transfer{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 5 * time.Millisecond):
			}
		}
		t, err := s.attemptTransfer(ctx, p)
		if err == nil || !isRetryable(err) {
			return t, err
		}
		lastErr = err
	}
	return Transfer{}, fmt.Errorf("transfer contended after %d attempts: %w", maxAttempts, lastErr)
}

// attemptTransfer runs one transaction for the transfer. Correctness comes
// from the ordered row locks below, not the isolation level.
func (s *Service) attemptTransfer(ctx context.Context, p NewTransferParams) (Transfer, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Transfer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	first, second := orderPair(p.DebitAccountID, p.CreditAccountID)
	locked := map[uuid.UUID]Account{}
	for _, id := range []uuid.UUID{first, second} {
		a, err := scanAccount(tx.QueryRow(ctx, accountByIDSQL+" FOR UPDATE", id))
		if err != nil {
			return Transfer{}, err
		}
		locked[id] = a
	}

	debit, credit := locked[p.DebitAccountID], locked[p.CreditAccountID]
	if debit.Currency != p.Currency || credit.Currency != p.Currency {
		return Transfer{}, ErrCurrencyMismatch
	}
	if !debit.AllowOverdraft && debit.Balance < p.Amount {
		return Transfer{}, ErrInsufficientFunds
	}

	newDebitBal := debit.Balance - p.Amount
	newCreditBal := credit.Balance + p.Amount

	t := Transfer{
		ID:              uuid.New(),
		IdempotencyKey:  p.IdempotencyKey,
		DebitAccountID:  p.DebitAccountID,
		CreditAccountID: p.CreditAccountID,
		Amount:          p.Amount,
		Currency:        p.Currency,
		Status:          "posted",
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO transfers (id, idempotency_key, debit_account_id, credit_account_id, amount, currency)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		t.ID, t.IdempotencyKey, t.DebitAccountID, t.CreditAccountID, t.Amount, t.Currency,
	).Scan(&t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost the race to a concurrent identical request; return its result.
			if existing, lookupErr := s.transferByKey(ctx, p.IdempotencyKey); lookupErr == nil && sameRequest(existing, p) {
				return existing, nil
			}
			return Transfer{}, ErrIdempotencyConflict
		}
		return Transfer{}, fmt.Errorf("insert transfer: %w", err)
	}

	if err := applyPosting(ctx, tx, t.ID, t.DebitAccountID, "debit", p.Amount, newDebitBal); err != nil {
		return Transfer{}, err
	}
	if err := applyPosting(ctx, tx, t.ID, t.CreditAccountID, "credit", p.Amount, newCreditBal); err != nil {
		return Transfer{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Transfer{}, fmt.Errorf("commit transfer: %w", err)
	}
	return t, nil
}

// GetTransfer returns a transfer by ID.
func (s *Service) GetTransfer(ctx context.Context, id uuid.UUID) (Transfer, error) {
	return scanTransfer(s.pool.QueryRow(ctx, transferSelect+` WHERE id = $1`, id))
}

// ListPostings returns an account's postings newest-first. A non-zero
// beforeID returns only postings with a lower ID (keyset pagination).
func (s *Service) ListPostings(ctx context.Context, accountID uuid.UUID, limit int, beforeID int64) ([]Posting, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if beforeID <= 0 {
		beforeID = int64(^uint64(0) >> 1) // max int64
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, transfer_id, account_id, direction, amount, balance_after, created_at
		FROM postings
		WHERE account_id = $1 AND id < $2
		ORDER BY id DESC
		LIMIT $3`, accountID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Posting
	for rows.Next() {
		var p Posting
		if err := rows.Scan(&p.ID, &p.TransferID, &p.AccountID, &p.Direction, &p.Amount, &p.BalanceAfter, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Ping verifies database connectivity for readiness checks.
func (s *Service) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

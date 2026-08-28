// Package ledger implements double-entry accounting over Postgres.
//
// Every money movement is a Transfer that produces exactly two Postings — one
// debit, one credit, equal in magnitude — applied atomically with the running
// balances of both accounts. Transfers are idempotent on a caller-supplied key
// and can be reversed exactly once with a compensating transfer.
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
	ID                 uuid.UUID  `json:"id"`
	IdempotencyKey     string     `json:"idempotency_key"`
	DebitAccountID     uuid.UUID  `json:"debit_account_id"`
	CreditAccountID    uuid.UUID  `json:"credit_account_id"`
	Amount             int64      `json:"amount"`
	Currency           string     `json:"currency"`
	Status             string     `json:"status"`
	ReversesTransferID *uuid.UUID `json:"reverses_transfer_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
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

// Page is a slice of results plus the cursor to fetch the next slice, or "" at
// the end.
type Page[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor"`
}

// Service is the ledger's public API. It is safe for concurrent use.
type Service struct {
	pool store.Pool
}

// New returns a Service backed by the given pool.
func New(pool store.Pool) *Service { return &Service{pool: pool} }

// Ping verifies database connectivity for readiness checks.
func (s *Service) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// --- accounts ---

// NewAccountParams are the inputs to CreateAccount.
type NewAccountParams struct {
	Name           string
	Currency       string
	AllowOverdraft bool
}

// CreateAccount opens a new account with a zero balance.
func (s *Service) CreateAccount(ctx context.Context, p NewAccountParams) (Account, error) {
	if p.Name == "" {
		return Account{}, invalid("name is required")
	}
	if !validCurrency(p.Currency) {
		return Account{}, invalid("currency must be a 3-letter ISO 4217 code")
	}
	a := Account{ID: uuid.New(), Name: p.Name, Currency: p.Currency, AllowOverdraft: p.AllowOverdraft}
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

// ListAccounts returns accounts newest-first, keyset-paginated on (created_at, id).
func (s *Service) ListAccounts(ctx context.Context, limit int, cursor string) (Page[Account], error) {
	limit = clampLimit(limit)
	ts, id, err := decodeCursor(cursor)
	if err != nil {
		return Page[Account]{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, currency, balance, allow_overdraft, created_at
		FROM accounts
		WHERE (created_at, id) < ($1, $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3`, ts, id, limit+1)
	if err != nil {
		return Page[Account]{}, err
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Name, &a.Currency, &a.Balance, &a.AllowOverdraft, &a.CreatedAt); err != nil {
			return Page[Account]{}, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return Page[Account]{}, err
	}
	return paginate(out, limit, func(a Account) (time.Time, uuid.UUID) { return a.CreatedAt, a.ID }), nil
}

// --- transfers ---

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
func (s *Service) CreateTransfer(ctx context.Context, p NewTransferParams) (Transfer, error) {
	if err := validateTransfer(p); err != nil {
		return Transfer{}, err
	}
	spec := transferSpec{
		key: p.IdempotencyKey, debit: p.DebitAccountID, credit: p.CreditAccountID,
		amount: p.Amount, currency: p.Currency,
	}
	return s.runTransfer(ctx, spec)
}

// ReverseTransfer creates a compensating transfer that moves the same amount
// back in the opposite direction and marks the original 'reversed'. It is
// idempotent on idempotencyKey. A transfer can be reversed at most once, and a
// reversal cannot itself be reversed.
func (s *Service) ReverseTransfer(ctx context.Context, originalID uuid.UUID, idempotencyKey string) (Transfer, error) {
	if idempotencyKey == "" {
		return Transfer{}, invalid("idempotency_key is required")
	}
	if existing, err := s.transferByKey(ctx, idempotencyKey); err == nil {
		if existing.ReversesTransferID == nil || *existing.ReversesTransferID != originalID {
			return Transfer{}, ErrIdempotencyConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrTransferNotFound) {
		return Transfer{}, err
	}

	original, err := s.GetTransfer(ctx, originalID)
	if err != nil {
		return Transfer{}, err
	}
	if original.ReversesTransferID != nil {
		return Transfer{}, ErrCannotReverseReversal
	}
	if original.Status == "reversed" {
		return Transfer{}, ErrAlreadyReversed
	}

	spec := transferSpec{
		key:      idempotencyKey,
		debit:    original.CreditAccountID, // swap direction
		credit:   original.DebitAccountID,
		amount:   original.Amount,
		currency: original.Currency,
		reverses: &originalID,
	}
	return s.runTransfer(ctx, spec)
}

// GetTransfer returns a transfer by ID.
func (s *Service) GetTransfer(ctx context.Context, id uuid.UUID) (Transfer, error) {
	return scanTransfer(s.pool.QueryRow(ctx, transferSelect+` WHERE id = $1`, id))
}

// ListTransfers returns transfers newest-first, keyset-paginated. When
// accountID is non-nil, only transfers touching that account are returned.
func (s *Service) ListTransfers(ctx context.Context, accountID *uuid.UUID, limit int, cursor string) (Page[Transfer], error) {
	limit = clampLimit(limit)
	ts, id, err := decodeCursor(cursor)
	if err != nil {
		return Page[Transfer]{}, err
	}
	rows, err := s.pool.Query(ctx, transferSelect+`
		WHERE (created_at, id) < ($1, $2)
		  AND ($3::uuid IS NULL OR debit_account_id = $3 OR credit_account_id = $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4`, ts, id, accountID, limit+1)
	if err != nil {
		return Page[Transfer]{}, err
	}
	defer rows.Close()

	var out []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return Page[Transfer]{}, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return Page[Transfer]{}, err
	}
	return paginate(out, limit, func(t Transfer) (time.Time, uuid.UUID) { return t.CreatedAt, t.ID }), nil
}

// --- postings ---

// ListPostings returns an account's postings newest-first. A non-zero beforeID
// returns only postings with a lower ID (keyset pagination on the serial id).
func (s *Service) ListPostings(ctx context.Context, accountID uuid.UUID, limit int, beforeID int64) ([]Posting, error) {
	limit = clampLimit(limit)
	if beforeID <= 0 {
		beforeID = maxInt64
	}
	rows, err := s.pool.Query(ctx, postingSelect+`
		WHERE account_id = $1 AND id < $2
		ORDER BY id DESC
		LIMIT $3`, accountID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	return scanPostings(rows)
}

// TransferPostings returns the two postings that make up a transfer (debit then
// credit), for an audit view.
func (s *Service) TransferPostings(ctx context.Context, transferID uuid.UUID) ([]Posting, error) {
	rows, err := s.pool.Query(ctx, postingSelect+`
		WHERE transfer_id = $1
		ORDER BY direction`, transferID)
	if err != nil {
		return nil, err
	}
	return scanPostings(rows)
}

// --- transfer machinery ---

// transferSpec is the internal, fully-resolved description of a transfer to
// post. reverses is set only for reversal transfers.
type transferSpec struct {
	key      string
	debit    uuid.UUID
	credit   uuid.UUID
	amount   int64
	currency string
	reverses *uuid.UUID
}

// runTransfer posts a transfer, retrying the transaction a bounded number of
// times on serialization failures (40001) and deadlocks (40P01).
func (s *Service) runTransfer(ctx context.Context, spec transferSpec) (Transfer, error) {
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
		t, err := s.attemptTransfer(ctx, spec)
		if err == nil || !isRetryable(err) {
			return t, err
		}
		lastErr = err
	}
	return Transfer{}, fmt.Errorf("transfer contended after %d attempts: %w", maxAttempts, lastErr)
}

// attemptTransfer runs one transaction for the transfer. Correctness comes from
// the ordered row locks below, not the isolation level.
func (s *Service) attemptTransfer(ctx context.Context, spec transferSpec) (Transfer, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Transfer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	first, second := orderPair(spec.debit, spec.credit)
	locked := map[uuid.UUID]Account{}
	for _, id := range []uuid.UUID{first, second} {
		a, err := scanAccount(tx.QueryRow(ctx, accountByIDSQL+" FOR UPDATE", id))
		if err != nil {
			return Transfer{}, err
		}
		locked[id] = a
	}

	debit, credit := locked[spec.debit], locked[spec.credit]
	if debit.Currency != spec.currency || credit.Currency != spec.currency {
		return Transfer{}, ErrCurrencyMismatch
	}
	if !debit.AllowOverdraft && debit.Balance < spec.amount {
		return Transfer{}, ErrInsufficientFunds
	}

	newDebitBal := debit.Balance - spec.amount
	newCreditBal := credit.Balance + spec.amount

	t := Transfer{
		ID: uuid.New(), IdempotencyKey: spec.key,
		DebitAccountID: spec.debit, CreditAccountID: spec.credit,
		Amount: spec.amount, Currency: spec.currency, Status: "posted",
		ReversesTransferID: spec.reverses,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO transfers
			(id, idempotency_key, debit_account_id, credit_account_id, amount, currency, reverses_transfer_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at`,
		t.ID, t.IdempotencyKey, t.DebitAccountID, t.CreditAccountID, t.Amount, t.Currency, spec.reverses,
	).Scan(&t.CreatedAt)
	if err != nil {
		return s.handleTransferInsertError(ctx, err, spec)
	}

	if spec.reverses != nil {
		tag, err := tx.Exec(ctx,
			`UPDATE transfers SET status = 'reversed' WHERE id = $1 AND status = 'posted'`, *spec.reverses)
		if err != nil {
			return Transfer{}, err
		}
		if tag.RowsAffected() != 1 {
			return Transfer{}, ErrAlreadyReversed
		}
	}

	if err := applyPosting(ctx, tx, t.ID, spec.debit, "debit", spec.amount, newDebitBal); err != nil {
		return Transfer{}, err
	}
	if err := applyPosting(ctx, tx, t.ID, spec.credit, "credit", spec.amount, newCreditBal); err != nil {
		return Transfer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Transfer{}, fmt.Errorf("commit transfer: %w", err)
	}
	return t, nil
}

// handleTransferInsertError disambiguates the two unique constraints that can
// trip on insert: the idempotency key and the one-reversal-per-original index.
func (s *Service) handleTransferInsertError(ctx context.Context, err error, spec transferSpec) (Transfer, error) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return Transfer{}, fmt.Errorf("insert transfer: %w", err)
	}
	if pgErr.ConstraintName == "transfers_one_reversal_per_original" {
		return Transfer{}, ErrAlreadyReversed
	}
	// Idempotency-key race: a concurrent identical request won. Return its result.
	existing, lookupErr := s.transferByKey(ctx, spec.key)
	if lookupErr != nil {
		return Transfer{}, ErrIdempotencyConflict
	}
	if !sameSpec(existing, spec) {
		return Transfer{}, ErrIdempotencyConflict
	}
	return existing, nil
}

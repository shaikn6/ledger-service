package ledger

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isRetryable reports whether err is a Postgres serialization failure (40001)
// or deadlock (40P01) — both safe to retry from the top of the transaction.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

const accountByIDSQL = `
	SELECT id, name, currency, balance, allow_overdraft, created_at
	FROM accounts WHERE id = $1`

const transferSelect = `
	SELECT id, idempotency_key, debit_account_id, credit_account_id, amount, currency, status, created_at
	FROM transfers`

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

func validCurrency(c string) bool { return currencyRE.MatchString(c) }

// row is satisfied by both pgx.Row and the row returned from a pool/tx query.
type row interface {
	Scan(dest ...any) error
}

func scanAccount(r row) (Account, error) {
	var a Account
	err := r.Scan(&a.ID, &a.Name, &a.Currency, &a.Balance, &a.AllowOverdraft, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	return a, err
}

func scanTransfer(r row) (Transfer, error) {
	var t Transfer
	err := r.Scan(&t.ID, &t.IdempotencyKey, &t.DebitAccountID, &t.CreditAccountID,
		&t.Amount, &t.Currency, &t.Status, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrTransferNotFound
	}
	return t, err
}

func (s *Service) transferByKey(ctx context.Context, key string) (Transfer, error) {
	return scanTransfer(s.pool.QueryRow(ctx, transferSelect+` WHERE idempotency_key = $1`, key))
}

func validateTransfer(p NewTransferParams) error {
	switch {
	case p.IdempotencyKey == "":
		return invalid("idempotency_key is required")
	case len(p.IdempotencyKey) > 255:
		return invalid("idempotency_key must be at most 255 characters")
	case p.DebitAccountID == uuid.Nil || p.CreditAccountID == uuid.Nil:
		return invalid("debit_account_id and credit_account_id are required")
	case p.DebitAccountID == p.CreditAccountID:
		return ErrSameAccount
	case p.Amount <= 0:
		return invalid("amount must be a positive integer in minor units")
	case !validCurrency(p.Currency):
		return invalid("currency must be a 3-letter ISO 4217 code")
	}
	return nil
}

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", ErrValidation, msg)
}

func sameRequest(t Transfer, p NewTransferParams) bool {
	return t.DebitAccountID == p.DebitAccountID &&
		t.CreditAccountID == p.CreditAccountID &&
		t.Amount == p.Amount &&
		t.Currency == p.Currency
}

// orderPair returns the two ids sorted so lock acquisition order is
// deterministic regardless of transfer direction.
func orderPair(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if bytesLess(a, b) {
		return a, b
	}
	return b, a
}

func bytesLess(a, b uuid.UUID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func applyPosting(ctx context.Context, tx pgx.Tx, transferID, accountID uuid.UUID, direction string, amount, balanceAfter int64) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO postings (transfer_id, account_id, direction, amount, balance_after)
		VALUES ($1, $2, $3, $4, $5)`,
		transferID, accountID, direction, amount, balanceAfter); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE accounts SET balance = $1, version = version + 1 WHERE id = $2`,
		balanceAfter, accountID)
	return err
}

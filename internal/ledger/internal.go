package ledger

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxInt64 = int64(^uint64(0) >> 1)

const accountByIDSQL = `
	SELECT id, name, currency, balance, allow_overdraft, created_at
	FROM accounts WHERE id = $1`

const transferSelect = `
	SELECT id, idempotency_key, debit_account_id, credit_account_id, amount, currency, status, reverses_transfer_id, created_at
	FROM transfers`

const postingSelect = `
	SELECT id, transfer_id, account_id, direction, amount, balance_after, created_at
	FROM postings`

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

func validCurrency(c string) bool { return currencyRE.MatchString(c) }

func clampLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

// isRetryable reports whether err is a Postgres serialization failure (40001)
// or deadlock (40P01) — both safe to retry from the top of the transaction.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// row is satisfied by both pgx.Row and pgx.Rows.
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
	var reverses pgtype.UUID
	err := r.Scan(&t.ID, &t.IdempotencyKey, &t.DebitAccountID, &t.CreditAccountID,
		&t.Amount, &t.Currency, &t.Status, &reverses, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrTransferNotFound
	}
	if err == nil && reverses.Valid {
		id := uuid.UUID(reverses.Bytes)
		t.ReversesTransferID = &id
	}
	return t, err
}

func scanPostings(rows pgx.Rows) ([]Posting, error) {
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

// sameSpec reports whether an existing transfer represents the same request as
// spec — used to tell an idempotent replay from a key collision.
func sameSpec(t Transfer, spec transferSpec) bool {
	return t.DebitAccountID == spec.debit &&
		t.CreditAccountID == spec.credit &&
		t.Amount == spec.amount &&
		t.Currency == spec.currency
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

// --- keyset pagination cursor ---
//
// A cursor is base64("<RFC3339Nano>|<uuid>"), pointing at the last row of the
// previous page. The empty cursor means "from the newest row".

func encodeCursor(ts time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	if cursor == "" {
		// Sentinel that sorts after every real row under "< (ts, id)".
		return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), uuid.Max, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	tsStr, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return ts, id, nil
}

// paginate trims an over-fetched slice (limit+1) to limit and derives the next
// cursor from the last kept row.
func paginate[T any](rows []T, limit int, key func(T) (time.Time, uuid.UUID)) Page[T] {
	p := Page[T]{Data: rows}
	if len(rows) > limit {
		p.Data = rows[:limit]
		ts, id := key(p.Data[len(p.Data)-1])
		p.NextCursor = encodeCursor(ts, id)
	}
	return p
}

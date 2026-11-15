package ledger

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// These tests exercise the pure, deterministic helpers in internal.go directly,
// without a database, so they run on every machine and in CI regardless of
// Postgres. They cover the lock-ordering the no-deadlock guarantee rests on,
// the keyset-pagination cursor (round trip and every malformed path) and the
// retry/validation predicates.

func TestOrderPair(t *testing.T) {
	// Pairs that differ at the first byte and at the last byte, so both ends
	// of the byte comparison are exercised.
	firstLo := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	firstHi := uuid.MustParse("ff000000-0000-0000-0000-000000000000")
	lastLo := uuid.MustParse("11111111-1111-1111-1111-111111111100")
	lastHi := uuid.MustParse("11111111-1111-1111-1111-111111111101")

	cases := []struct{ a, b uuid.UUID }{
		{firstLo, firstHi},
		{lastLo, lastHi},
	}
	for _, c := range cases {
		lo, hi := orderPair(c.a, c.b)
		// Lowest id is always first.
		if !bytesLess(lo, hi) {
			t.Fatalf("orderPair(%s,%s) = (%s,%s): first is not the lower id", c.a, c.b, lo, hi)
		}
		// Order is independent of argument order: this is what makes two
		// opposite-direction transfers lock the same pair in the same order.
		lo2, hi2 := orderPair(c.b, c.a)
		if lo2 != lo || hi2 != hi {
			t.Fatalf("orderPair not symmetric: (%s,%s) vs reversed (%s,%s)", lo, hi, lo2, hi2)
		}
	}

	// Equal ids: neither is "less"; order is stable.
	lo, hi := orderPair(firstLo, firstLo)
	if lo != firstLo || hi != firstLo {
		t.Fatalf("orderPair of equal ids = (%s,%s), want both %s", lo, hi, firstLo)
	}
	if bytesLess(firstLo, firstLo) {
		t.Fatal("bytesLess reported an id is less than itself")
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{-1, 50},
		{0, 50},
		{1, 1},
		{50, 50},
		{200, 200},
		{201, 50},
	}
	for _, c := range cases {
		if got := clampLimit(c.in); got != c.want {
			t.Fatalf("clampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"serialization failure 40001", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock 40P01", &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped serialization failure", fmt.Errorf("tx: %w", &pgconn.PgError{Code: "40001"}), true},
		{"other pg error", &pgconn.PgError{Code: "23505"}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.err); got != c.want {
				t.Fatalf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestSameSpec(t *testing.T) {
	debit := uuid.MustParse("10000000-0000-0000-0000-000000000000")
	credit := uuid.MustParse("20000000-0000-0000-0000-000000000000")
	base := transferSpec{debit: debit, credit: credit, amount: 2500, currency: "USD"}
	tr := Transfer{DebitAccountID: debit, CreditAccountID: credit, Amount: 2500, Currency: "USD"}

	if !sameSpec(tr, base) {
		t.Fatal("identical transfer and spec should match")
	}

	other := uuid.MustParse("30000000-0000-0000-0000-000000000000")
	mismatches := map[string]transferSpec{
		"debit":    {debit: other, credit: credit, amount: 2500, currency: "USD"},
		"credit":   {debit: debit, credit: other, amount: 2500, currency: "USD"},
		"amount":   {debit: debit, credit: credit, amount: 2501, currency: "USD"},
		"currency": {debit: debit, credit: credit, amount: 2500, currency: "EUR"},
	}
	for field, spec := range mismatches {
		if sameSpec(tr, spec) {
			t.Fatalf("spec differing in %s should not match", field)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	// Non-UTC input with nanosecond precision: encode normalises to UTC and
	// decode must recover the same instant and id.
	ts := time.Date(2026, 3, 1, 12, 30, 45, 123456789, time.FixedZone("UTC+1", 3600))
	id := uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789")

	ts2, id2, err := decodeCursor(encodeCursor(ts, id))
	if err != nil {
		t.Fatalf("decodeCursor after encodeCursor: %v", err)
	}
	if !ts2.Equal(ts) {
		t.Fatalf("round-trip ts = %s, want %s (same instant)", ts2, ts)
	}
	if id2 != id {
		t.Fatalf("round-trip id = %s, want %s", id2, id)
	}
}

func TestDecodeCursorEmptySentinel(t *testing.T) {
	ts, id, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor err = %v, want nil", err)
	}
	want := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if !ts.Equal(want) {
		t.Fatalf("empty cursor ts = %s, want sentinel %s", ts, want)
	}
	if id != uuid.Max {
		t.Fatalf("empty cursor id = %s, want uuid.Max", id)
	}
}

func TestDecodeCursorMalformed(t *testing.T) {
	validTS := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
	validID := uuid.MustParse("abcdef01-2345-6789-abcd-ef0123456789").String()
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	cases := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"no separator", enc("no-pipe-here")},
		{"bad timestamp", enc("not-a-time|" + validID)},
		{"bad uuid", enc(validTS + "|not-a-uuid")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := decodeCursor(c.cursor); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("decodeCursor(%q) err = %v, want ErrInvalidCursor", c.cursor, err)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	type rowT struct {
		ts time.Time
		id uuid.UUID
	}
	key := func(r rowT) (time.Time, uuid.UUID) { return r.ts, r.id }
	mk := func(sec int) rowT {
		return rowT{
			ts: time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC),
			id: uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-0000000000%02d", sec)),
		}
	}

	// At the limit: no next cursor.
	full := paginate([]rowT{mk(1), mk(2)}, 2, key)
	if len(full.Data) != 2 || full.NextCursor != "" {
		t.Fatalf("at limit: len=%d cursor=%q, want 2 and empty", len(full.Data), full.NextCursor)
	}

	// Over-fetched (limit+1): trimmed to limit, cursor points at the last kept row.
	rows := []rowT{mk(1), mk(2), mk(3)}
	page := paginate(rows, 2, key)
	if len(page.Data) != 2 {
		t.Fatalf("over limit: len=%d, want 2", len(page.Data))
	}
	if page.NextCursor == "" {
		t.Fatal("over limit: expected a next cursor")
	}
	ts, id, err := decodeCursor(page.NextCursor)
	if err != nil {
		t.Fatalf("decode next cursor: %v", err)
	}
	last := rows[1] // the last row kept on this page
	if !ts.Equal(last.ts) || id != last.id {
		t.Fatalf("next cursor points at (%s,%s), want last kept row (%s,%s)", ts, id, last.ts, last.id)
	}
}

func TestValidateTransferBranches(t *testing.T) {
	ok := func() NewTransferParams { return validParams(100) }

	cases := []struct {
		name    string
		mutate  func(*NewTransferParams)
		wantErr error
	}{
		{"missing idempotency key", func(p *NewTransferParams) { p.IdempotencyKey = "" }, ErrValidation},
		{"idempotency key too long", func(p *NewTransferParams) { p.IdempotencyKey = string(make([]byte, 256)) }, ErrValidation},
		{"nil debit account", func(p *NewTransferParams) { p.DebitAccountID = uuid.Nil }, ErrValidation},
		{"same account", func(p *NewTransferParams) { p.CreditAccountID = p.DebitAccountID }, ErrSameAccount},
		{"non-positive amount", func(p *NewTransferParams) { p.Amount = 0 }, ErrValidation},
		{"bad currency", func(p *NewTransferParams) { p.Currency = "usd" }, ErrValidation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := ok()
			c.mutate(&p)
			if err := validateTransfer(p); !errors.Is(err, c.wantErr) {
				t.Fatalf("validateTransfer err = %v, want %v", err, c.wantErr)
			}
		})
	}

	if err := validateTransfer(ok()); err != nil {
		t.Fatalf("valid params should pass, got %v", err)
	}
}

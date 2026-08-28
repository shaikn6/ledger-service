package ledger_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/shaikn6/ledger-service/internal/ledger"
	"github.com/shaikn6/ledger-service/internal/store"
)

// setup connects to TEST_DATABASE_URL, runs migrations, and truncates all
// ledger tables so each test starts clean. Tests are skipped when the env var
// is unset (e.g. a local run without Postgres); CI always sets it.
func setup(t *testing.T) *ledger.Service {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, dsn, store.PoolConfig{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE postings, transfers, accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return ledger.New(pool)
}

func mkAccount(t *testing.T, s *ledger.Service, currency string, overdraft bool) ledger.Account {
	t.Helper()
	a, err := s.CreateAccount(context.Background(), ledger.NewAccountParams{
		Name: "acct-" + currency, Currency: currency, AllowOverdraft: overdraft,
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return a
}

func fund(t *testing.T, s *ledger.Service, dst ledger.Account, amount int64) {
	t.Helper()
	src, err := s.CreateAccount(context.Background(), ledger.NewAccountParams{
		Name: "funding", Currency: dst.Currency, AllowOverdraft: true,
	})
	if err != nil {
		t.Fatalf("funding account: %v", err)
	}
	_, err = s.CreateTransfer(context.Background(), ledger.NewTransferParams{
		IdempotencyKey:  "fund-" + dst.ID.String(),
		DebitAccountID:  src.ID,
		CreditAccountID: dst.ID,
		Amount:          amount,
		Currency:        dst.Currency,
	})
	if err != nil {
		t.Fatalf("fund transfer: %v", err)
	}
}

func balance(t *testing.T, s *ledger.Service, id uuid.UUID) int64 {
	t.Helper()
	a, err := s.GetAccount(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	return a.Balance
}

func TestTransfer_MovesFundsAndBalances(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", false)
	dst := mkAccount(t, s, "USD", false)
	fund(t, s, src, 10_000)

	tr, err := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "t1", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 2_500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}
	if tr.Status != "posted" {
		t.Fatalf("status = %q", tr.Status)
	}
	if got := balance(t, s, src.ID); got != 7_500 {
		t.Fatalf("src balance = %d, want 7500", got)
	}
	if got := balance(t, s, dst.ID); got != 2_500 {
		t.Fatalf("dst balance = %d, want 2500", got)
	}

	postings, err := s.ListPostings(ctx, src.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListPostings: %v", err)
	}
	if len(postings) == 0 || postings[0].Direction != "debit" || postings[0].BalanceAfter != 7_500 {
		t.Fatalf("unexpected postings: %+v", postings)
	}
}

func TestTransfer_InsufficientFunds(t *testing.T) {
	s := setup(t)
	src := mkAccount(t, s, "USD", false)
	dst := mkAccount(t, s, "USD", false)
	fund(t, s, src, 100)

	_, err := s.CreateTransfer(context.Background(), ledger.NewTransferParams{
		IdempotencyKey: "t2", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 500, Currency: "USD",
	})
	if err != ledger.ErrInsufficientFunds {
		t.Fatalf("err = %v, want ErrInsufficientFunds", err)
	}
	if got := balance(t, s, src.ID); got != 100 {
		t.Fatalf("src balance changed to %d", got)
	}
}

func TestTransfer_CurrencyMismatch(t *testing.T) {
	s := setup(t)
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "EUR", false)

	_, err := s.CreateTransfer(context.Background(), ledger.NewTransferParams{
		IdempotencyKey: "t3", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 100, Currency: "USD",
	})
	if err != ledger.ErrCurrencyMismatch {
		t.Fatalf("err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestTransfer_IdempotentReplay(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)

	params := ledger.NewTransferParams{
		IdempotencyKey: "same-key", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 300, Currency: "USD",
	}
	first, err := s.CreateTransfer(ctx, params)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.CreateTransfer(ctx, params)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay produced a new transfer: %s != %s", first.ID, second.ID)
	}
	if got := balance(t, s, dst.ID); got != 300 {
		t.Fatalf("replay moved funds twice: dst balance = %d", got)
	}
}

func TestTransfer_IdempotencyConflict(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)

	_, err := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "k", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 100, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err = s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "k", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 999, Currency: "USD", // different amount, same key
	})
	if err != ledger.ErrIdempotencyConflict {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
}

// TestTransfer_ConcurrentIntegrity fires many concurrent transfers in both
// directions between two accounts and asserts the combined balance is
// conserved and never goes negative — the property serializable isolation plus
// ordered locking is there to guarantee.
func TestTransfer_ConcurrentIntegrity(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := mkAccount(t, s, "USD", false)
	b := mkAccount(t, s, "USD", false)
	fund(t, s, a, 100_000)
	fund(t, s, b, 100_000)

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from, to := a, b
			if i%2 == 1 {
				from, to = b, a
			}
			_, err := s.CreateTransfer(ctx, ledger.NewTransferParams{
				IdempotencyKey:  uuid.NewString(),
				DebitAccountID:  from.ID,
				CreditAccountID: to.ID,
				Amount:          1_000,
				Currency:        "USD",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && err != ledger.ErrInsufficientFunds {
			t.Fatalf("concurrent transfer: %v", err)
		}
	}

	ba, bb := balance(t, s, a.ID), balance(t, s, b.ID)
	if ba+bb != 200_000 {
		t.Fatalf("balance not conserved: %d + %d = %d, want 200000", ba, bb, ba+bb)
	}
	if ba < 0 || bb < 0 {
		t.Fatalf("negative balance: a=%d b=%d", ba, bb)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	s := setup(t)
	_, err := s.GetAccount(context.Background(), uuid.New())
	if err != ledger.ErrAccountNotFound {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
}

func TestCreateAccount_Validation(t *testing.T) {
	s := setup(t)
	_, err := s.CreateAccount(context.Background(), ledger.NewAccountParams{Name: "", Currency: "USD"})
	if err == nil {
		t.Fatal("expected validation error for empty name")
	}
	_, err = s.CreateAccount(context.Background(), ledger.NewAccountParams{Name: "x", Currency: "usd"})
	if err == nil {
		t.Fatal("expected validation error for lowercase currency")
	}
}

func TestReverseTransfer_RestoresBalances(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)

	orig, err := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "orig-1", DebitAccountID: src.ID, CreditAccountID: dst.ID,
		Amount: 4_000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}

	rev, err := s.ReverseTransfer(ctx, orig.ID, "rev-1")
	if err != nil {
		t.Fatalf("ReverseTransfer: %v", err)
	}
	if rev.ReversesTransferID == nil || *rev.ReversesTransferID != orig.ID {
		t.Fatalf("reversal not linked to original: %+v", rev)
	}
	if rev.DebitAccountID != dst.ID || rev.CreditAccountID != src.ID {
		t.Fatalf("reversal direction not swapped: %+v", rev)
	}
	if got := balance(t, s, src.ID); got != 0 {
		t.Fatalf("src balance after reversal = %d, want 0", got)
	}
	if got := balance(t, s, dst.ID); got != 0 {
		t.Fatalf("dst balance after reversal = %d, want 0", got)
	}

	reloaded, err := s.GetTransfer(ctx, orig.ID)
	if err != nil {
		t.Fatalf("GetTransfer: %v", err)
	}
	if reloaded.Status != "reversed" {
		t.Fatalf("original status = %q, want reversed", reloaded.Status)
	}
}

func TestReverseTransfer_Idempotent(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)
	orig, _ := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "o", DebitAccountID: src.ID, CreditAccountID: dst.ID, Amount: 100, Currency: "USD",
	})

	first, err := s.ReverseTransfer(ctx, orig.ID, "rk")
	if err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	second, err := s.ReverseTransfer(ctx, orig.ID, "rk")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("replay created a second reversal")
	}
	if got := balance(t, s, dst.ID); got != 0 {
		t.Fatalf("replay moved funds again: dst = %d", got)
	}
}

func TestReverseTransfer_AlreadyReversed(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)
	orig, _ := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "o", DebitAccountID: src.ID, CreditAccountID: dst.ID, Amount: 100, Currency: "USD",
	})
	if _, err := s.ReverseTransfer(ctx, orig.ID, "r1"); err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	// A new key, but the original is already reversed.
	if _, err := s.ReverseTransfer(ctx, orig.ID, "r2"); err != ledger.ErrAlreadyReversed {
		t.Fatalf("err = %v, want ErrAlreadyReversed", err)
	}
}

func TestReverseTransfer_CannotReverseAReversal(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	src := mkAccount(t, s, "USD", true)
	dst := mkAccount(t, s, "USD", false)
	orig, _ := s.CreateTransfer(ctx, ledger.NewTransferParams{
		IdempotencyKey: "o", DebitAccountID: src.ID, CreditAccountID: dst.ID, Amount: 100, Currency: "USD",
	})
	rev, err := s.ReverseTransfer(ctx, orig.ID, "r1")
	if err != nil {
		t.Fatalf("reversal: %v", err)
	}
	if _, err := s.ReverseTransfer(ctx, rev.ID, "r2"); err != ledger.ErrCannotReverseReversal {
		t.Fatalf("err = %v, want ErrCannotReverseReversal", err)
	}
}

func TestListAccounts_Pagination(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		mkAccount(t, s, "USD", false)
	}
	first, err := s.ListAccounts(ctx, 2, "")
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(first.Data) != 2 || first.NextCursor == "" {
		t.Fatalf("page 1 = %d rows, cursor %q", len(first.Data), first.NextCursor)
	}
	second, err := s.ListAccounts(ctx, 2, first.NextCursor)
	if err != nil {
		t.Fatalf("ListAccounts page 2: %v", err)
	}
	if len(second.Data) != 2 {
		t.Fatalf("page 2 = %d rows", len(second.Data))
	}
	if first.Data[0].ID == second.Data[0].ID {
		t.Fatalf("pages overlap")
	}

	if _, err := s.ListAccounts(ctx, 2, "not-a-valid-cursor!!"); err != ledger.ErrInvalidCursor {
		t.Fatalf("bad cursor err = %v, want ErrInvalidCursor", err)
	}
}

func TestListTransfers_FilterByAccount(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := mkAccount(t, s, "USD", true)
	b := mkAccount(t, s, "USD", true)
	c := mkAccount(t, s, "USD", true)
	mustTransfer(t, s, a, b, 100, "t-ab")
	mustTransfer(t, s, b, c, 100, "t-bc")

	page, err := s.ListTransfers(ctx, &a.ID, 10, "")
	if err != nil {
		t.Fatalf("ListTransfers: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].IdempotencyKey != "t-ab" {
		t.Fatalf("filter by account a returned %+v", page.Data)
	}

	all, _ := s.ListTransfers(ctx, nil, 10, "")
	if len(all.Data) != 2 {
		t.Fatalf("unfiltered returned %d transfers", len(all.Data))
	}
}

func TestTransferPostings(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := mkAccount(t, s, "USD", true)
	b := mkAccount(t, s, "USD", true)
	tr := mustTransfer(t, s, a, b, 700, "tp")

	postings, err := s.TransferPostings(ctx, tr.ID)
	if err != nil {
		t.Fatalf("TransferPostings: %v", err)
	}
	if len(postings) != 2 {
		t.Fatalf("expected 2 postings, got %d", len(postings))
	}
	if postings[0].Direction != "credit" || postings[1].Direction != "debit" {
		t.Fatalf("postings not ordered by direction: %+v", postings)
	}
}

func mustTransfer(t *testing.T, s *ledger.Service, from, to ledger.Account, amount int64, key string) ledger.Transfer {
	t.Helper()
	tr, err := s.CreateTransfer(context.Background(), ledger.NewTransferParams{
		IdempotencyKey: key, DebitAccountID: from.ID, CreditAccountID: to.ID,
		Amount: amount, Currency: from.Currency,
	})
	if err != nil {
		t.Fatalf("mustTransfer(%s): %v", key, err)
	}
	return tr
}

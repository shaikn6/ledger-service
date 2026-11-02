package ledger

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// validParams returns transfer params that pass every check except the one a
// test deliberately breaks, so validateTransfer can be exercised without a DB.
func validParams(amount int64) NewTransferParams {
	return NewTransferParams{
		IdempotencyKey:  "k",
		DebitAccountID:  uuid.New(),
		CreditAccountID: uuid.New(),
		Amount:          amount,
		Currency:        "USD",
	}
}

func TestValidateTransferAmountBounds(t *testing.T) {
	if err := validateTransfer(validParams(1)); err != nil {
		t.Fatalf("amount 1 should be valid, got %v", err)
	}
	if err := validateTransfer(validParams(MaxAmount)); err != nil {
		t.Fatalf("amount MaxAmount should be valid, got %v", err)
	}
	// An absurd value must be a validation error, not reach the DB (which
	// would overflow bigint on accumulation and surface as an opaque 500).
	if err := validateTransfer(validParams(MaxAmount + 1)); !errors.Is(err, ErrValidation) {
		t.Fatalf("amount over MaxAmount = %v, want ErrValidation", err)
	}
	if err := validateTransfer(validParams(maxInt64)); !errors.Is(err, ErrValidation) {
		t.Fatalf("amount maxInt64 = %v, want ErrValidation", err)
	}
}

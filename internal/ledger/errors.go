package ledger

import "errors"

// Sentinel errors returned by the service. Handlers map these to HTTP status
// codes and stable error codes; nothing else leaks to the client.
var (
	ErrAccountNotFound     = errors.New("account not found")
	ErrTransferNotFound    = errors.New("transfer not found")
	ErrCurrencyMismatch    = errors.New("transfer and account currencies do not match")
	ErrInsufficientFunds   = errors.New("insufficient funds in debit account")
	ErrSameAccount         = errors.New("debit and credit accounts must differ")
	ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
	ErrValidation          = errors.New("validation failed")
)

// Package money represents monetary amounts as integer minor units in a
// single currency. Floating point is never used for money.
package money

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidCurrency is returned for a currency code that is not three
// uppercase ASCII letters (ISO 4217 shape).
var ErrInvalidCurrency = errors.New("currency must be a 3-letter ISO 4217 code")

// ErrInvalidAmount is returned for a non-positive or unparseable amount.
var ErrInvalidAmount = errors.New("amount must be a positive integer in minor units")

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

// Amount is a signed quantity of minor units (e.g. cents) plus a currency.
type Amount struct {
	MinorUnits int64
	Currency   string
}

// New validates and constructs an Amount. minorUnits must be > 0.
func New(minorUnits int64, currency string) (Amount, error) {
	if !currencyRE.MatchString(currency) {
		return Amount{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	if minorUnits <= 0 {
		return Amount{}, ErrInvalidAmount
	}
	return Amount{MinorUnits: minorUnits, Currency: currency}, nil
}

// Parse builds an Amount from a decimal string ("10.00") or a bare minor-unit
// integer string ("1000"), given a currency and its number of fraction digits.
func Parse(s, currency string, fractionDigits int) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Amount{}, ErrInvalidAmount
	}
	neg := strings.HasPrefix(s, "-")

	whole, frac, hasDot := strings.Cut(s, ".")
	if !hasDot {
		v, err := strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return Amount{}, ErrInvalidAmount
		}
		return New(v, currency)
	}
	if len(frac) > fractionDigits {
		return Amount{}, fmt.Errorf("%w: too many fraction digits for %s", ErrInvalidAmount, currency)
	}
	frac += strings.Repeat("0", fractionDigits-len(frac))

	wv, err := strconv.ParseInt(strings.TrimPrefix(whole, "-"), 10, 64)
	if err != nil {
		return Amount{}, ErrInvalidAmount
	}
	fv, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return Amount{}, ErrInvalidAmount
	}
	scale := pow10(fractionDigits)
	minor := wv*scale + fv
	if neg {
		minor = -minor
	}
	return New(minor, currency)
}

// String renders the amount as a decimal in major units with the given
// number of fraction digits.
func (a Amount) String(fractionDigits int) string {
	scale := pow10(fractionDigits)
	sign := ""
	v := a.MinorUnits
	if v < 0 {
		sign, v = "-", -v
	}
	if fractionDigits == 0 {
		return fmt.Sprintf("%s%d %s", sign, v, a.Currency)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, v/scale, fractionDigits, v%scale, a.Currency)
}

func pow10(n int) int64 {
	r := int64(1)
	for i := 0; i < n; i++ {
		r *= 10
	}
	return r
}

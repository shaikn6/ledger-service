package money

import (
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		minor    int64
		currency string
		wantErr  error
	}{
		{"valid", 1000, "USD", nil},
		{"zero rejected", 0, "USD", ErrInvalidAmount},
		{"negative rejected", -5, "USD", ErrInvalidAmount},
		{"lowercase currency rejected", 100, "usd", ErrInvalidCurrency},
		{"long currency rejected", 100, "USDD", ErrInvalidCurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.minor, tt.currency)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("New(%d,%q) err = %v, want %v", tt.minor, tt.currency, err, tt.wantErr)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in       string
		currency string
		frac     int
		want     int64
		wantErr  bool
	}{
		{"10.00", "USD", 2, 1000, false},
		{"10.5", "USD", 2, 1050, false},
		{"1000", "USD", 2, 1000, false},
		{"0.01", "USD", 2, 1, false},
		{"5", "JPY", 0, 5, false},
		{"10.001", "USD", 2, 0, true}, // too many fraction digits
		{"-10.00", "USD", 2, 0, true}, // non-positive
		{"abc", "USD", 2, 0, true},    // unparseable
		{"", "USD", 2, 0, true},       // empty
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in, tt.currency, tt.frac)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) expected error, got %v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if got.MinorUnits != tt.want {
				t.Fatalf("Parse(%q) = %d, want %d", tt.in, got.MinorUnits, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	a := Amount{MinorUnits: 123456, Currency: "USD"}
	if got := a.String(2); got != "1234.56 USD" {
		t.Fatalf("String(2) = %q", got)
	}
	if got := (Amount{MinorUnits: 5, Currency: "JPY"}).String(0); got != "5 JPY" {
		t.Fatalf("String(0) = %q", got)
	}
	if got := (Amount{MinorUnits: -700, Currency: "USD"}).String(2); got != "-7.00 USD" {
		t.Fatalf("negative String(2) = %q", got)
	}
}

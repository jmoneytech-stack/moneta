package plaid

import (
	"errors"
	"math"
	"testing"
)

func TestMoneyToCentsRoundsHalfAwayFromZeroExactly(t *testing.T) {
	tests := []struct {
		name   string
		amount float64
		want   int64
	}{
		{name: "four thirty five precision trap", amount: 4.35, want: 435},
		{name: "one point zero zero five", amount: 1.005, want: 101},
		{name: "negative one point zero zero five", amount: -1.005, want: -101},
		{name: "two point six seven five", amount: 2.675, want: 268},
		{name: "negative two point six seven five", amount: -2.675, want: -268},
		{name: "below half", amount: 1.004, want: 100},
		{name: "above half", amount: 1.006, want: 101},
		{name: "half cent", amount: 0.005, want: 1},
		{name: "negative half cent", amount: -0.005, want: -1},
		{name: "twenty nine cents", amount: 0.29, want: 29},
		{name: "zero", amount: 0, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := moneyToCents(test.amount)
			if err != nil {
				t.Fatalf("moneyToCents() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("moneyToCents(%v) = %d, want %d", test.amount, got, test.want)
			}
		})
	}
}

func TestTransactionAmountToCentsAppliesCanonicalSign(t *testing.T) {
	tests := []struct {
		name   string
		amount float64
		want   int64
	}{
		{name: "Plaid outflow", amount: 4.35, want: -435},
		{name: "Plaid inflow", amount: -12.34, want: 1234},
		{name: "zero", amount: 0, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := transactionAmountToCents(test.amount)
			if err != nil {
				t.Fatalf("transactionAmountToCents() error: %v", err)
			}
			if got != test.want {
				t.Fatalf(
					"transactionAmountToCents(%v) = %d, want %d",
					test.amount,
					got,
					test.want,
				)
			}
		})
	}
}

func TestMoneyToCentsRejectsInvalidAndOutOfRangeValues(t *testing.T) {
	for name, amount := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := moneyToCents(amount)
			if !errors.Is(err, ErrAmountInvalid) {
				t.Fatalf("moneyToCents() error = %v, want ErrAmountInvalid", err)
			}
		})
	}

	for name, amount := range map[string]float64{
		"positive overflow": math.MaxFloat64,
		"negative overflow": -math.MaxFloat64,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := moneyToCents(amount)
			if !errors.Is(err, ErrAmountOutOfRange) {
				t.Fatalf("moneyToCents() error = %v, want ErrAmountOutOfRange", err)
			}
		})
	}
}

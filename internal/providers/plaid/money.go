package plaid

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
)

var (
	ErrAmountInvalid    = errors.New("Plaid amount must be finite")
	ErrAmountOutOfRange = errors.New("Plaid amount is outside the int64 cents range")
)

// moneyToCents converts a Plaid SDK float exactly once at the provider
// boundary. FormatFloat recovers the shortest decimal representation of the
// source value, then decimal digit arithmetic rounds half away from zero.
func moneyToCents(amount float64) (int64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, ErrAmountInvalid
	}

	decimal := strconv.FormatFloat(amount, 'f', -1, 64)
	negative := strings.HasPrefix(decimal, "-")
	if negative {
		decimal = strings.TrimPrefix(decimal, "-")
	}
	whole, fraction, found := strings.Cut(decimal, ".")
	if !found {
		fraction = ""
	}
	for len(fraction) < 2 {
		fraction += "0"
	}

	cents := new(big.Int)
	if _, ok := cents.SetString(whole+fraction[:2], 10); !ok {
		return 0, ErrAmountInvalid
	}
	if len(fraction) > 2 && fraction[2] >= '5' {
		cents.Add(cents, big.NewInt(1))
	}
	if negative {
		cents.Neg(cents)
	}
	if !cents.IsInt64() {
		return 0, ErrAmountOutOfRange
	}
	return cents.Int64(), nil
}

// transactionAmountToCents also applies Plaid's transaction sign convention:
// positive Plaid amounts are outflows, while Moneta stores outflows as negative.
func transactionAmountToCents(amount float64) (int64, error) {
	cents, err := moneyToCents(amount)
	if err != nil {
		return 0, err
	}
	if cents == math.MinInt64 {
		return 0, ErrAmountOutOfRange
	}
	return -cents, nil
}

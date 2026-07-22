package cli

import (
	"math/big"
	"strings"

	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// Ratio returns numerator/denominator as a canonical decimal truncated toward
// zero to decimalPlaces. A non-positive denominator or negative precision
// returns nil. Big integers avoid overflow and float precision loss.
func Ratio(numerator, denominator int64, decimalPlaces int) *toon.Number {
	if denominator <= 0 || decimalPlaces < 0 {
		return nil
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimalPlaces)), nil)
	scaledNumerator := new(big.Int).Mul(big.NewInt(numerator), scale)
	scaled := new(big.Int).Quo(scaledNumerator, big.NewInt(denominator))
	value := scaledNumber(scaled, decimalPlaces)
	return &value
}

// ScaledInteger renders value / 10^decimalPlaces as a canonical decimal.
// It is useful for rates held as integer basis points. Negative precision
// returns zero rather than emitting an invalid number.
func ScaledInteger(value int64, decimalPlaces int) toon.Number {
	if decimalPlaces < 0 {
		return "0"
	}
	return scaledNumber(big.NewInt(value), decimalPlaces)
}

func scaledNumber(value *big.Int, decimalPlaces int) toon.Number {
	negative := value.Sign() < 0
	magnitude := new(big.Int).Abs(new(big.Int).Set(value)).String()
	if len(magnitude) <= decimalPlaces {
		magnitude = strings.Repeat("0", decimalPlaces-len(magnitude)+1) + magnitude
	}
	split := len(magnitude) - decimalPlaces
	whole := magnitude[:split]
	fraction := strings.TrimRight(magnitude[split:], "0")
	formatted := whole
	if fraction != "" {
		formatted += "." + fraction
	}
	if negative && formatted != "0" {
		formatted = "-" + formatted
	}
	return toon.Number(formatted)
}

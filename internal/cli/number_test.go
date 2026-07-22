package cli

import (
	"math"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/toon"
)

func TestRatio(t *testing.T) {
	tests := []struct {
		name        string
		numerator   int64
		denominator int64
		places      int
		want        string
		wantNil     bool
	}{
		{"card utilization", 340000, 1000000, 4, "0.34", false},
		{"truncates recurring fraction", 1, 3, 4, "0.3333", false},
		{"negative ratio", -1, 3, 4, "-0.3333", false},
		{"integer boundary", math.MaxInt64, 1, 4, "9223372036854775807", false},
		{"zero denominator", 1, 0, 4, "", true},
		{"negative denominator", 1, -1, 4, "", true},
		{"negative precision", 1, 1, -1, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Ratio(test.numerator, test.denominator, test.places)
			if test.wantNil {
				if got != nil {
					t.Errorf("Ratio() = %q, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("Ratio() = nil")
			}
			if string(*got) != test.want {
				t.Errorf("Ratio() = %q, want %q", *got, test.want)
			}
			if !toon.ValidNumber(*got) {
				t.Errorf("Ratio() = %q is not canonical", *got)
			}
		})
	}
}

func TestScaledInteger(t *testing.T) {
	tests := []struct {
		value  int64
		places int
		want   string
	}{
		{2299, 4, "0.2299"},
		{500, 4, "0.05"},
		{0, 4, "0"},
		{-125, 2, "-1.25"},
		{42, 0, "42"},
		{42, -1, "0"},
	}
	for _, test := range tests {
		got := ScaledInteger(test.value, test.places)
		if string(got) != test.want {
			t.Errorf("ScaledInteger(%d, %d) = %q, want %q", test.value, test.places, got, test.want)
		}
		if !toon.ValidNumber(got) {
			t.Errorf("ScaledInteger() = %q is not canonical", got)
		}
	}
}

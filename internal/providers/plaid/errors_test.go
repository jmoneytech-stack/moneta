package plaid

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsLoginRequired(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"login required code", &APIError{Code: "ITEM_LOGIN_REQUIRED"}, true},
		{"wrapped login required", fmt.Errorf("sync provider item: %w", &APIError{Code: "ITEM_LOGIN_REQUIRED"}), true},
		{"other api code", &APIError{Code: "RATE_LIMIT_EXCEEDED"}, false},
		{"empty code", &APIError{}, false},
		{"plain error", errors.New("boom"), false},
		{"request failed sentinel", ErrRequestFailed, false},
		{"nil", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsLoginRequired(test.err); got != test.want {
				t.Errorf("IsLoginRequired(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

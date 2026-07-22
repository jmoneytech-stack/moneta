package plaid

import (
	"context"
	"errors"
	"fmt"

	plaidSDK "github.com/plaid/plaid-go/v43/plaid"
)

const (
	errorItemLoginRequired = "ITEM_LOGIN_REQUIRED"
	errorSyncMutation      = "TRANSACTIONS_SYNC_MUTATION_DURING_PAGINATION"
)

var ErrRequestFailed = errors.New("Plaid request failed")

// APIError is the safe subset of a Plaid API error. It deliberately omits the
// provider message, request body, credentials, and access token.
type APIError struct {
	Code string
}

func (e *APIError) Error() string {
	if e == nil || e.Code == "" {
		return ErrRequestFailed.Error()
	}
	return fmt.Sprintf("Plaid API error: %s", e.Code)
}

func sanitizeSDKError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextError := ctx.Err(); contextError != nil {
		return contextError
	}

	plaidError, conversionError := plaidSDK.ToPlaidError(err)
	if conversionError == nil && plaidError.GetErrorCode() != "" {
		return &APIError{
			Code: plaidError.GetErrorCode(),
		}
	}
	return ErrRequestFailed
}

// IsLoginRequired reports whether err carries the reauth-class Plaid error
// code ITEM_LOGIN_REQUIRED - the same code Connections() maps to the
// login_required state. The CLI and Connections() classify through this one
// helper so they can never disagree.
func IsLoginRequired(err error) bool {
	return errorCode(err) == errorItemLoginRequired
}

func errorCode(err error) string {
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return ""
}

func liabilitiesUnavailable(err error) bool {
	switch errorCode(err) {
	case "NO_LIABILITY_ACCOUNTS",
		"PRODUCTS_NOT_SUPPORTED",
		"PRODUCT_NOT_ENABLED",
		"ACCESS_NOT_GRANTED",
		"ADDITIONAL_CONSENT_REQUIRED",
		// Transient: the product's initial pull is still running shortly
		// after link. Liabilities arrive on a later sync; no retry loop.
		"PRODUCT_NOT_READY":
		return true
	default:
		return false
	}
}

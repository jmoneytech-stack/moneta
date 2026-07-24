package store

import (
	"context"
	"database/sql"
)

// ReadCards loads credit-card accounts only, with the same latest balance and
// terms mapping as ReadDebts; loan accounts stay in ReadDebts. It reuses the
// DebtReport shape, so Debts holds the card rows and TotalDebtCents and
// MissingBalance cover cards alone. Balances keep their stored sign: positive
// when owed, negative when the card is overpaid.
func ReadCards(ctx context.Context, db *sql.DB) (DebtReport, error) {
	return readLiabilities(ctx, db, liabilityScopeCards, "cards")
}

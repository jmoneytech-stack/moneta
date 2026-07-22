package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CashflowFilter selects one inclusive period. From and To are required
// valid YYYY-MM-DD dates. Account is an optional case-insensitive literal
// substring of the account name.
type CashflowFilter struct {
	From    string
	To      string
	Account string
}

// CashflowSummary aggregates posted, non-excluded rows in one period.
// InflowCents and OutflowCents are positive magnitudes. NetCents is signed:
// inflow minus outflow.
type CashflowSummary struct {
	Count        int
	InflowCents  int64
	OutflowCents int64
	NetCents     int64
}

func validateCashflowFilter(filter CashflowFilter) error {
	if filter.From == "" || filter.To == "" {
		return fmt.Errorf("cashflow filter requires from and to dates")
	}
	return validateTransactionFilter(TransactionFilter{
		From:    filter.From,
		To:      filter.To,
		Account: filter.Account,
	})
}

// ReadCashflow returns one consistent snapshot of posted, non-excluded
// cashflow. Refunds and other positive rows count as inflow; negative rows
// count as positive outflow magnitude. Zero-amount rows count as matching
// rows but affect no money total.
func ReadCashflow(
	ctx context.Context,
	db *sql.DB,
	filter CashflowFilter,
) (CashflowSummary, error) {
	var summary CashflowSummary
	if db == nil {
		return summary, fmt.Errorf("database is required")
	}
	if err := validateCashflowFilter(filter); err != nil {
		return summary, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, fmt.Errorf("begin cashflow read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	account := escapeLikeLiteral(filter.Account)
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN transactions.amount_cents > 0
				THEN transactions.amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN transactions.amount_cents < 0
				THEN -transactions.amount_cents ELSE 0 END), 0),
			COALESCE(SUM(transactions.amount_cents), 0)
		FROM transactions
		JOIN accounts ON accounts.id = transactions.account_id
		WHERE transactions.date >= ?
		  AND transactions.date <= ?
		  AND transactions.status = 'posted'
		  AND transactions.excluded = 0
		  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
	`, filter.From, filter.To, account, account).Scan(
		&summary.Count,
		&summary.InflowCents,
		&summary.OutflowCents,
		&summary.NetCents,
	); err != nil {
		return summary, fmt.Errorf("summarize cashflow: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return summary, fmt.Errorf("commit cashflow read: %w", err)
	}
	return summary, nil
}

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// PortfolioUtilization contains the current credit-card portfolio utilization
// inputs. Only cards with both a balance snapshot and a positive usable limit
// contribute. All zero means utilization is undefined.
type PortfolioUtilization struct {
	Cards      int
	DebtCents  int64
	LimitCents int64
}

// utilizationContribution is the single portfolio-utilization definition
// shared by the utilization trend and the dashboard: a non-NULL snapshot
// limit wins over the current credit_terms limit, a card with no positive
// usable limit is excluded, and a negative (overpaid) balance contributes
// zero debt rather than reducing the portfolio.
func utilizationContribution(
	balanceCents int64,
	snapshotLimit *int64,
	termsLimit *int64,
) (debtCents, limitCents int64, ok bool) {
	limit := snapshotLimit
	if limit == nil {
		limit = termsLimit
	}
	if limit == nil || *limit <= 0 {
		return 0, 0, false
	}
	debt := balanceCents
	if debt < 0 {
		debt = 0
	}
	return debt, *limit, true
}

// ReadPortfolioUtilization applies the shared definition to each credit card's
// latest balance snapshot. Cards without a snapshot are excluded. Per-card
// utilization in 'moneta cards'/'moneta debts' is a separate view over
// credit_terms and is unaffected.
func ReadPortfolioUtilization(
	ctx context.Context,
	db *sql.DB,
) (PortfolioUtilization, error) {
	var portfolio PortfolioUtilization
	if db == nil {
		return portfolio, fmt.Errorf("database is required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return portfolio, fmt.Errorf("begin portfolio utilization read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		WITH ranked_balances AS (
			SELECT
				balance_snapshots.account_id,
				balance_snapshots.current_cents,
				balance_snapshots.limit_cents,
				ROW_NUMBER() OVER (
					PARTITION BY balance_snapshots.account_id
					ORDER BY balance_snapshots.date DESC, balance_snapshots.id DESC
				) AS row_number
			FROM balance_snapshots
		)
		SELECT
			ranked_balances.current_cents,
			ranked_balances.limit_cents,
			credit_terms.limit_cents
		FROM accounts
		JOIN ranked_balances
			ON ranked_balances.account_id = accounts.id
			AND ranked_balances.row_number = 1
		LEFT JOIN credit_terms ON credit_terms.account_id = accounts.id
		WHERE accounts.type = 'credit_card'
		ORDER BY accounts.id
	`)
	if err != nil {
		return portfolio, fmt.Errorf("read portfolio utilization balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var balance int64
		var snapshotLimit, termsLimit sql.NullInt64
		if err := rows.Scan(&balance, &snapshotLimit, &termsLimit); err != nil {
			return portfolio, fmt.Errorf("scan portfolio utilization balance: %w", err)
		}
		var snapshotPtr, termsPtr *int64
		if snapshotLimit.Valid {
			value := snapshotLimit.Int64
			snapshotPtr = &value
		}
		if termsLimit.Valid {
			value := termsLimit.Int64
			termsPtr = &value
		}
		debt, limit, ok := utilizationContribution(balance, snapshotPtr, termsPtr)
		if !ok {
			continue
		}
		if err := addTrendCents(&portfolio.DebtCents, debt); err != nil {
			return portfolio, err
		}
		if err := addTrendCents(&portfolio.LimitCents, limit); err != nil {
			return portfolio, err
		}
		portfolio.Cards++
	}
	if err := rows.Err(); err != nil {
		return portfolio, fmt.Errorf("read portfolio utilization balances: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return portfolio, fmt.Errorf("commit portfolio utilization read: %w", err)
	}
	return portfolio, nil
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// DebtRow is one credit-card or loan account with its latest balance and
// best-effort terms. BalanceCents is a positive debt magnitude. APRBasisPoints
// is a decimal-fraction rate scaled by 10,000: 2299 renders as 0.2299.
type DebtRow struct {
	Name           string
	Type           string
	BalanceCents   *int64
	LimitCents     *int64
	APRBasisPoints *int64
	DueDay         *int
}

// DebtReport contains every liability account and summary totals. Accounts
// without a balance remain in Debts and MissingBalance but do not contribute
// to TotalDebtCents.
type DebtReport struct {
	Count          int
	TotalDebtCents int64
	MissingBalance int
	Debts          []DebtRow
}

// ReadDebts loads every credit-card and loan account, its latest balance, and
// matching terms in one query inside one read transaction. It performs no
// provider calls and selects no credentials.
func ReadDebts(ctx context.Context, db *sql.DB) (DebtReport, error) {
	var report DebtReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin debts read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		WITH ranked_balances AS (
			SELECT
				balance_snapshots.account_id,
				balance_snapshots.current_cents,
				ROW_NUMBER() OVER (
					PARTITION BY balance_snapshots.account_id
					ORDER BY balance_snapshots.date DESC, balance_snapshots.id DESC
				) AS row_number
			FROM balance_snapshots
		),
		latest_balances AS (
			SELECT account_id, current_cents
			FROM ranked_balances
			WHERE row_number = 1
		)
		SELECT
			accounts.name,
			accounts.type,
			latest_balances.current_cents,
			credit_terms.limit_cents,
			CASE
				WHEN accounts.type = 'credit_card' THEN credit_terms.apr
				ELSE loan_terms.apr
			END,
			CASE
				WHEN accounts.type = 'credit_card' THEN credit_terms.due_day
				ELSE NULL
			END
		FROM accounts
		LEFT JOIN latest_balances ON latest_balances.account_id = accounts.id
		LEFT JOIN credit_terms ON credit_terms.account_id = accounts.id
		LEFT JOIN loan_terms ON loan_terms.account_id = accounts.id
		WHERE accounts.type IN ('credit_card', 'loan')
		ORDER BY accounts.name, accounts.id
	`)
	if err != nil {
		return report, fmt.Errorf("read debts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var debt DebtRow
		var balance sql.NullInt64
		var limit sql.NullInt64
		var apr sql.NullFloat64
		var dueDay sql.NullInt64
		if err := rows.Scan(&debt.Name, &debt.Type, &balance, &limit, &apr, &dueDay); err != nil {
			return report, fmt.Errorf("scan debt: %w", err)
		}
		if balance.Valid {
			amount := balance.Int64
			if amount == math.MinInt64 {
				return report, fmt.Errorf("debt balance magnitude overflows integer cents")
			}
			if amount < 0 {
				amount = -amount
			}
			debt.BalanceCents = &amount
			if err := addDebtCents(&report.TotalDebtCents, amount); err != nil {
				return report, err
			}
		} else {
			report.MissingBalance++
		}
		if limit.Valid {
			value := limit.Int64
			debt.LimitCents = &value
		}
		if apr.Valid {
			basisPoints, err := aprPercentToBasisPoints(apr.Float64)
			if err != nil {
				return report, fmt.Errorf("read APR for %s account: %w", debt.Type, err)
			}
			debt.APRBasisPoints = &basisPoints
		}
		if dueDay.Valid {
			value := int(dueDay.Int64)
			debt.DueDay = &value
		}
		report.Debts = append(report.Debts, debt)
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read debts: %w", err)
	}
	report.Count = len(report.Debts)
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit debts read: %w", err)
	}
	return report, nil
}

// APR values enter the schema as percentage points from Plaid (22.99 means
// 22.99%). The read boundary converts to a decimal fraction rounded to one
// basis point, so 22.99 becomes the scaled integer 2299 and renders as 0.2299.
func aprPercentToBasisPoints(percent float64) (int64, error) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) {
		return 0, fmt.Errorf("APR must be finite")
	}
	scaled := percent * 100
	if scaled >= float64(math.MaxInt64) || scaled < float64(math.MinInt64) {
		return 0, fmt.Errorf("APR exceeds supported range")
	}
	return int64(math.Round(scaled)), nil
}

func addDebtCents(total *int64, amount int64) error {
	if amount > 0 && *total > math.MaxInt64-amount {
		return fmt.Errorf("total debt overflows integer cents")
	}
	*total += amount
	return nil
}

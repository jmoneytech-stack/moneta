package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

// NetworthFilter selects the latest balance per account, optionally capped at
// one inclusive YYYY-MM-DD date. An empty AsOf means latest without a cutoff.
type NetworthFilter struct {
	AsOf string
}

// NetworthTypeSummary is one canonical account-type aggregate. Count includes
// accounts without a snapshot; BalancedCount identifies how many contributed
// to BalanceCents. Liability balances preserve the canonical sign.
type NetworthTypeSummary struct {
	Type          string
	Count         int
	BalancedCount int
	BalanceCents  int64
}

// NetworthReport is an as-of balance snapshot. Asset and liability balances
// retain their canonical sign, and NetworthCents is AssetsCents minus
// LiabilitiesCents. Accounts without an eligible balance
// are counted in MissingBalance and omitted from all money totals.
type NetworthReport struct {
	AsOf             string
	AssetsCents      int64
	LiabilitiesCents int64
	NetworthCents    int64
	Accounts         int
	MissingBalance   int
	ByType           []NetworthTypeSummary
}

func validateNetworthFilter(filter NetworthFilter) error {
	if filter.AsOf == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", filter.AsOf)
	if err != nil || parsed.Format("2006-01-02") != filter.AsOf {
		return fmt.Errorf("networth as-of date %q must use valid YYYY-MM-DD form", filter.AsOf)
	}
	return nil
}

func isLiabilityAccountType(accountType string) bool {
	switch canon.AccountType(accountType) {
	case canon.AccountTypeCreditCard, canon.AccountTypeLoan:
		return true
	default:
		return false
	}
}

// ReadNetworth loads every account and its latest eligible balance in one
// query inside one read transaction. The default report AsOf is the newest
// selected balance date across accounts; an explicit cutoff is echoed even
// when no account has an eligible snapshot.
func ReadNetworth(
	ctx context.Context,
	db *sql.DB,
	filter NetworthFilter,
) (NetworthReport, error) {
	report := NetworthReport{AsOf: filter.AsOf}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	if err := validateNetworthFilter(filter); err != nil {
		return report, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin networth read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		WITH ranked_balances AS (
			SELECT
				balance_snapshots.account_id,
				balance_snapshots.date,
				balance_snapshots.current_cents,
				ROW_NUMBER() OVER (
					PARTITION BY balance_snapshots.account_id
					ORDER BY balance_snapshots.date DESC, balance_snapshots.id DESC
				) AS row_number
			FROM balance_snapshots
			WHERE (? = '' OR balance_snapshots.date <= ?)
		),
		latest_balances AS (
			SELECT account_id, date, current_cents
			FROM ranked_balances
			WHERE row_number = 1
		)
		SELECT accounts.type, latest_balances.date, latest_balances.current_cents
		FROM accounts
		LEFT JOIN latest_balances ON latest_balances.account_id = accounts.id
		ORDER BY CASE accounts.type
			WHEN 'checking' THEN 0
			WHEN 'savings' THEN 1
			WHEN 'investment' THEN 2
			WHEN 'asset' THEN 3
			WHEN 'credit_card' THEN 4
			WHEN 'loan' THEN 5
			ELSE 6
		END, accounts.id
	`, filter.AsOf, filter.AsOf)
	if err != nil {
		return report, fmt.Errorf("read networth balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var accountType string
		var balanceDate sql.NullString
		var balance sql.NullInt64
		if err := rows.Scan(&accountType, &balanceDate, &balance); err != nil {
			return report, fmt.Errorf("scan networth balance: %w", err)
		}

		report.Accounts++
		if len(report.ByType) == 0 || report.ByType[len(report.ByType)-1].Type != accountType {
			report.ByType = append(report.ByType, NetworthTypeSummary{Type: accountType})
		}
		group := &report.ByType[len(report.ByType)-1]
		group.Count++
		if !balance.Valid {
			report.MissingBalance++
			continue
		}
		group.BalancedCount++
		if filter.AsOf == "" && balanceDate.String > report.AsOf {
			report.AsOf = balanceDate.String
		}

		amount := balance.Int64
		if isLiabilityAccountType(accountType) {
			if amount == math.MinInt64 {
				return report, fmt.Errorf("liability balance magnitude overflows integer cents")
			}
			if err := addNetworthCents(&report.LiabilitiesCents, amount); err != nil {
				return report, err
			}
		} else {
			if err := addNetworthCents(&report.AssetsCents, amount); err != nil {
				return report, err
			}
		}
		if err := addNetworthCents(&group.BalanceCents, amount); err != nil {
			return report, err
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read networth balances: %w", err)
	}

	report.NetworthCents, err = subtractNetworthCents(report.AssetsCents, report.LiabilitiesCents)
	if err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit networth read: %w", err)
	}
	return report, nil
}

func subtractNetworthCents(assets, liabilities int64) (int64, error) {
	if (liabilities > 0 && assets < math.MinInt64+liabilities) ||
		(liabilities < 0 && assets > math.MaxInt64+liabilities) {
		return 0, fmt.Errorf("networth total overflows integer cents")
	}
	return assets - liabilities, nil
}

func addNetworthCents(total *int64, amount int64) error {
	if (amount > 0 && *total > math.MaxInt64-amount) ||
		(amount < 0 && *total < math.MinInt64-amount) {
		return fmt.Errorf("networth balance total overflows integer cents")
	}
	*total += amount
	return nil
}

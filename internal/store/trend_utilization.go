package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TrendUtilizationFilter selects an inclusive daily credit-utilization window
// and an optional literal account-name substring.
type TrendUtilizationFilter struct {
	From    string
	To      string
	Account string
}

// TrendUtilizationPoint is one day's carried-forward credit-card portfolio.
// HasUtilization is false when no card has both a balance snapshot and a
// positive usable limit that day.
type TrendUtilizationPoint struct {
	Date           string
	DebtCents      int64
	LimitCents     int64
	Accounts       int
	HasUtilization bool
}

// TrendUtilizationReport contains one point for every day in the requested
// window. Accounts counts matched credit-card accounts. MissingLimitDays
// counts days where at least one card has a carried balance but no positive
// usable limit.
type TrendUtilizationReport struct {
	From             string
	To               string
	Days             int
	Accounts         int
	MissingLimitDays int
	Points           []TrendUtilizationPoint
}

type trendUtilizationSnapshot struct {
	date         string
	currentCents int64
	limitCents   *int64
}

type trendUtilizationAccount struct {
	fallbackLimit *int64
	snapshots     []trendUtilizationSnapshot
	index         int
	current       trendUtilizationSnapshot
	hasSnapshot   bool
}

func validateTrendUtilizationFilter(
	filter TrendUtilizationFilter,
) (time.Time, time.Time, error) {
	if filter.From == "" || filter.To == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("utilization trend from and to dates are required")
	}
	from, err := time.Parse("2006-01-02", filter.From)
	if err != nil || from.Format("2006-01-02") != filter.From {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"utilization trend from date %q must use valid YYYY-MM-DD form",
			filter.From,
		)
	}
	to, err := time.Parse("2006-01-02", filter.To)
	if err != nil || to.Format("2006-01-02") != filter.To {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"utilization trend to date %q must use valid YYYY-MM-DD form",
			filter.To,
		)
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("utilization trend from date must not be after to date")
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > maxNetworthHistoryDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"utilization trend must not exceed %d days",
			maxNetworthHistoryDays,
		)
	}
	return from, to, nil
}

// ReadTrendUtilization computes a daily credit-card portfolio series. One
// query loads the latest pre-window snapshot and all in-window snapshots for
// every matched card. Snapshot limits win when non-NULL; a NULL snapshot limit
// falls back to credit_terms. Non-positive limits are excluded, and positive
// limits pair a zero debt contribution with zero or negative card balances.
func ReadTrendUtilization(
	ctx context.Context,
	db *sql.DB,
	filter TrendUtilizationFilter,
) (TrendUtilizationReport, error) {
	report := TrendUtilizationReport{From: filter.From, To: filter.To}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	from, to, err := validateTrendUtilizationFilter(filter)
	if err != nil {
		return report, err
	}
	report.Days = int(to.Sub(from).Hours()/24) + 1
	report.Points = make([]TrendUtilizationPoint, 0, report.Days)

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin utilization trend read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	account := escapeLikeLiteral(filter.Account)
	rows, err := tx.QueryContext(ctx, `
		WITH ranked_prior AS (
			SELECT
				balance_snapshots.id,
				balance_snapshots.account_id,
				balance_snapshots.date,
				balance_snapshots.current_cents,
				balance_snapshots.limit_cents,
				ROW_NUMBER() OVER (
					PARTITION BY balance_snapshots.account_id
					ORDER BY balance_snapshots.date DESC, balance_snapshots.id DESC
				) AS row_number
			FROM balance_snapshots
			WHERE balance_snapshots.date < ?
		),
		relevant_balances AS (
			SELECT id, account_id, date, current_cents, limit_cents
			FROM ranked_prior
			WHERE row_number = 1
			UNION ALL
			SELECT id, account_id, date, current_cents, limit_cents
			FROM balance_snapshots
			WHERE date BETWEEN ? AND ?
		)
		SELECT
			accounts.id,
			relevant_balances.date,
			relevant_balances.current_cents,
			relevant_balances.limit_cents,
			credit_terms.limit_cents
		FROM accounts
		LEFT JOIN relevant_balances ON relevant_balances.account_id = accounts.id
		LEFT JOIN credit_terms ON credit_terms.account_id = accounts.id
		WHERE accounts.type = 'credit_card'
		  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
		ORDER BY accounts.id, relevant_balances.date, relevant_balances.id
	`, filter.From, filter.From, filter.To, account, account)
	if err != nil {
		return report, fmt.Errorf("read utilization trend balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var accounts []trendUtilizationAccount
	var previousAccountID int64
	for rows.Next() {
		var accountID int64
		var date sql.NullString
		var current, snapshotLimit, fallbackLimit sql.NullInt64
		if err := rows.Scan(
			&accountID,
			&date,
			&current,
			&snapshotLimit,
			&fallbackLimit,
		); err != nil {
			return report, fmt.Errorf("scan utilization trend balance: %w", err)
		}
		if len(accounts) == 0 || accountID != previousAccountID {
			entry := trendUtilizationAccount{}
			if fallbackLimit.Valid {
				value := fallbackLimit.Int64
				entry.fallbackLimit = &value
			}
			accounts = append(accounts, entry)
			previousAccountID = accountID
		}
		if date.Valid && current.Valid {
			snapshot := trendUtilizationSnapshot{
				date:         date.String,
				currentCents: current.Int64,
			}
			if snapshotLimit.Valid {
				value := snapshotLimit.Int64
				snapshot.limitCents = &value
			}
			entry := &accounts[len(accounts)-1]
			entry.snapshots = append(entry.snapshots, snapshot)
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read utilization trend balances: %w", err)
	}
	report.Accounts = len(accounts)

	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		point := TrendUtilizationPoint{Date: date}
		missingLimit := false
		for index := range accounts {
			entry := &accounts[index]
			for entry.index < len(entry.snapshots) && entry.snapshots[entry.index].date <= date {
				entry.current = entry.snapshots[entry.index]
				entry.hasSnapshot = true
				entry.index++
			}
			if !entry.hasSnapshot {
				continue
			}
			limit := entry.current.limitCents
			if limit == nil {
				limit = entry.fallbackLimit
			}
			if limit == nil || *limit <= 0 {
				missingLimit = true
				continue
			}
			if err := addTrendCents(&point.LimitCents, *limit); err != nil {
				return report, err
			}
			if entry.current.currentCents > 0 {
				if err := addTrendCents(&point.DebtCents, entry.current.currentCents); err != nil {
					return report, err
				}
			}
			point.Accounts++
		}
		point.HasUtilization = point.Accounts > 0
		if missingLimit {
			report.MissingLimitDays++
		}
		report.Points = append(report.Points, point)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit utilization trend read: %w", err)
	}
	return report, nil
}

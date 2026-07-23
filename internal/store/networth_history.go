package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const maxNetworthHistoryDays = 3660

// NetworthHistoryFilter selects an inclusive local-calendar date window.
type NetworthHistoryFilter struct {
	From string
	To   string
}

// NetworthHistoryPoint is one day's carried-forward networth aggregate.
type NetworthHistoryPoint struct {
	Date             string
	AssetsCents      int64
	LiabilitiesCents int64
	NetworthCents    int64
}

// NetworthHistoryReport contains one point for every day in the requested
// inclusive window. HasBalances distinguishes an all-zero empty history from
// a real history whose values happen to total zero.
type NetworthHistoryReport struct {
	From        string
	To          string
	Days        int
	Points      []NetworthHistoryPoint
	HasBalances bool
}

// ResolveNetworthHistoryWindow parses Nd as exactly N local-calendar days,
// ending on now's local date and including both endpoints.
func ResolveNetworthHistoryWindow(value string, now time.Time) (NetworthHistoryFilter, error) {
	if len(value) < 2 || !strings.HasSuffix(value, "d") {
		return NetworthHistoryFilter{}, fmt.Errorf("must use Nd form with an integer day count, got %q", value)
	}
	days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
	if err != nil {
		return NetworthHistoryFilter{}, fmt.Errorf("must use Nd form with an integer day count, got %q", value)
	}
	if days < 1 {
		return NetworthHistoryFilter{}, fmt.Errorf("must be at least 1 day")
	}
	if days > maxNetworthHistoryDays {
		return NetworthHistoryFilter{}, fmt.Errorf("must not exceed %d days", maxNetworthHistoryDays)
	}
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := end.AddDate(0, 0, -(days - 1))
	return NetworthHistoryFilter{
		From: start.Format("2006-01-02"),
		To:   end.Format("2006-01-02"),
	}, nil
}

func validateNetworthHistoryFilter(filter NetworthHistoryFilter) (time.Time, time.Time, error) {
	if filter.From == "" || filter.To == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("networth history from and to dates are required")
	}
	from, err := time.Parse("2006-01-02", filter.From)
	if err != nil || from.Format("2006-01-02") != filter.From {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"networth history from date %q must use valid YYYY-MM-DD form",
			filter.From,
		)
	}
	to, err := time.Parse("2006-01-02", filter.To)
	if err != nil || to.Format("2006-01-02") != filter.To {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"networth history to date %q must use valid YYYY-MM-DD form",
			filter.To,
		)
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("networth history from date must not be after to date")
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > maxNetworthHistoryDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"networth history must not exceed %d days",
			maxNetworthHistoryDays,
		)
	}
	return from, to, nil
}

type networthHistorySnapshot struct {
	date  string
	cents int64
}

type networthHistoryAccount struct {
	typeName  string
	snapshots []networthHistorySnapshot
	index     int
	current   int64
	hasValue  bool
}

// ReadNetworthHistory computes a daily series from immutable account balance
// snapshots. One query loads each account's latest pre-window baseline plus
// every in-window snapshot; the daily walk carries balances forward in Go.
func ReadNetworthHistory(
	ctx context.Context,
	db *sql.DB,
	filter NetworthHistoryFilter,
) (NetworthHistoryReport, error) {
	report := NetworthHistoryReport{From: filter.From, To: filter.To}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	from, to, err := validateNetworthHistoryFilter(filter)
	if err != nil {
		return report, err
	}
	report.Days = int(to.Sub(from).Hours()/24) + 1
	report.Points = make([]NetworthHistoryPoint, 0, report.Days)

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin networth history read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		WITH ranked_prior AS (
			SELECT
				balance_snapshots.id,
				balance_snapshots.account_id,
				balance_snapshots.date,
				balance_snapshots.current_cents,
				ROW_NUMBER() OVER (
					PARTITION BY balance_snapshots.account_id
					ORDER BY balance_snapshots.date DESC, balance_snapshots.id DESC
				) AS row_number
			FROM balance_snapshots
			WHERE balance_snapshots.date < ?
		),
		relevant_balances AS (
			SELECT id, account_id, date, current_cents
			FROM ranked_prior
			WHERE row_number = 1
			UNION ALL
			SELECT id, account_id, date, current_cents
			FROM balance_snapshots
			WHERE date BETWEEN ? AND ?
		)
		SELECT
			accounts.id,
			accounts.type,
			relevant_balances.date,
			relevant_balances.current_cents
		FROM accounts
		LEFT JOIN relevant_balances ON relevant_balances.account_id = accounts.id
		ORDER BY accounts.id, relevant_balances.date, relevant_balances.id
	`, filter.From, filter.From, filter.To)
	if err != nil {
		return report, fmt.Errorf("read networth history balances: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var accounts []networthHistoryAccount
	var previousAccountID int64
	for rows.Next() {
		var accountID int64
		var accountType string
		var date sql.NullString
		var cents sql.NullInt64
		if err := rows.Scan(&accountID, &accountType, &date, &cents); err != nil {
			return report, fmt.Errorf("scan networth history balance: %w", err)
		}
		if len(accounts) == 0 || accountID != previousAccountID {
			accounts = append(accounts, networthHistoryAccount{typeName: accountType})
			previousAccountID = accountID
		}
		if date.Valid && cents.Valid {
			account := &accounts[len(accounts)-1]
			account.snapshots = append(account.snapshots, networthHistorySnapshot{
				date:  date.String,
				cents: cents.Int64,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read networth history balances: %w", err)
	}

	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		point := NetworthHistoryPoint{Date: date}
		for index := range accounts {
			account := &accounts[index]
			for account.index < len(account.snapshots) && account.snapshots[account.index].date <= date {
				account.current = account.snapshots[account.index].cents
				account.hasValue = true
				account.index++
			}
			if !account.hasValue {
				continue
			}
			report.HasBalances = true
			if isLiabilityAccountType(account.typeName) {
				if account.current == math.MinInt64 {
					return report, fmt.Errorf("liability balance magnitude overflows integer cents")
				}
				if err := addNetworthCents(&point.LiabilitiesCents, account.current); err != nil {
					return report, err
				}
			} else if err := addNetworthCents(&point.AssetsCents, account.current); err != nil {
				return report, err
			}
		}
		point.NetworthCents, err = subtractNetworthCents(point.AssetsCents, point.LiabilitiesCents)
		if err != nil {
			return report, err
		}
		report.Points = append(report.Points, point)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit networth history read: %w", err)
	}
	return report, nil
}

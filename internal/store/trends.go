package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

// TrendMoMPeriod is one selected calendar month and its preceding month.
type TrendMoMPeriod struct {
	ThisFrom string
	ThisTo   string
	PrevFrom string
	PrevTo   string
}

// TrendMoMFilter selects the two inclusive periods and an optional literal
// account-name substring used by the month-over-month spend comparison.
type TrendMoMFilter struct {
	ThisFrom string
	ThisTo   string
	PrevFrom string
	PrevTo   string
	Account  string
}

// TrendMoMCategory compares one canonical category across two months.
type TrendMoMCategory struct {
	Name           string
	SpendThisCents int64
	SpendPrevCents int64
	DeltaCents     int64
}

// TrendMoMReport contains full-period totals plus category rows. CategoryTotal
// is independent of any row limit.
type TrendMoMReport struct {
	SpendThisCents int64
	SpendPrevCents int64
	DeltaCents     int64
	Categories     []TrendMoMCategory
	CategoryTotal  int
}

// ResolveTrendMoMPeriod selects periodValue, or now's local calendar month
// when omitted, and computes the immediately preceding calendar month.
func ResolveTrendMoMPeriod(periodValue string, now time.Time) (TrendMoMPeriod, error) {
	if periodValue == "" {
		periodValue = now.Format("2006-01")
	}
	month, err := time.Parse("2006-01", periodValue)
	if err != nil || month.Format("2006-01") != periodValue {
		return TrendMoMPeriod{}, fmt.Errorf("period must use valid YYYY-MM form, got %q", periodValue)
	}
	thisFrom := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, now.Location())
	thisTo := thisFrom.AddDate(0, 1, -1)
	prevFrom := thisFrom.AddDate(0, -1, 0)
	prevTo := thisFrom.AddDate(0, 0, -1)
	return TrendMoMPeriod{
		ThisFrom: thisFrom.Format("2006-01-02"),
		ThisTo:   thisTo.Format("2006-01-02"),
		PrevFrom: prevFrom.Format("2006-01-02"),
		PrevTo:   prevTo.Format("2006-01-02"),
	}, nil
}

func validateTrendMoMFilter(filter TrendMoMFilter) error {
	if err := validateSpendFilter(SpendFilter{
		From: filter.ThisFrom, To: filter.ThisTo, Account: filter.Account,
	}); err != nil {
		return fmt.Errorf("validate current trend period: %w", err)
	}
	if err := validateSpendFilter(SpendFilter{
		From: filter.PrevFrom, To: filter.PrevTo, Account: filter.Account,
	}); err != nil {
		return fmt.Errorf("validate previous trend period: %w", err)
	}
	if filter.PrevTo >= filter.ThisFrom {
		return fmt.Errorf("previous trend period must end before current trend period")
	}
	return nil
}

type trendCategoryKey struct {
	valid bool
	id    int64
}

type trendCategoryAggregate struct {
	key      trendCategoryKey
	category TrendMoMCategory
}

// ReadTrendMoM computes posted, non-excluded category outflow deltas for two
// periods. Source transaction amounts remain signed cents; presentation spend
// is their checked positive magnitude. A limit <= 0 returns every category.
func ReadTrendMoM(
	ctx context.Context,
	db *sql.DB,
	filter TrendMoMFilter,
	limit int,
) (TrendMoMReport, error) {
	var report TrendMoMReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	if err := validateTrendMoMFilter(filter); err != nil {
		return report, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin month-over-month trend read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	account := escapeLikeLiteral(filter.Account)
	rows, err := tx.QueryContext(ctx, `
		SELECT
			categories.id,
			CASE
				WHEN categories.id IS NULL THEN 'Uncategorized'
				ELSE categories.name
			END,
			transactions.date,
			transactions.amount_cents
		FROM transactions
		JOIN accounts ON accounts.id = transactions.account_id
		LEFT JOIN categories ON categories.id = transactions.category_id
		WHERE (
			(transactions.date >= ? AND transactions.date <= ?)
			OR (transactions.date >= ? AND transactions.date <= ?)
		)
		  AND transactions.status = 'posted'
		  AND transactions.excluded = 0
		  AND transactions.amount_cents < 0
		  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
		ORDER BY transactions.date, transactions.id
	`, filter.PrevFrom, filter.PrevTo, filter.ThisFrom, filter.ThisTo, account, account)
	if err != nil {
		return report, fmt.Errorf("read month-over-month trend transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byCategory := make(map[trendCategoryKey]*trendCategoryAggregate)
	for rows.Next() {
		var categoryID sql.NullInt64
		var name, date string
		var amount int64
		if err := rows.Scan(&categoryID, &name, &date, &amount); err != nil {
			return report, fmt.Errorf("scan month-over-month trend transaction: %w", err)
		}
		if amount == math.MinInt64 {
			return report, fmt.Errorf("spend magnitude overflows integer cents")
		}
		spend := -amount
		key := trendCategoryKey{valid: categoryID.Valid, id: categoryID.Int64}
		aggregate := byCategory[key]
		if aggregate == nil {
			aggregate = &trendCategoryAggregate{
				key:      key,
				category: TrendMoMCategory{Name: name},
			}
			byCategory[key] = aggregate
		}
		if date >= filter.ThisFrom && date <= filter.ThisTo {
			if err := addTrendCents(&aggregate.category.SpendThisCents, spend); err != nil {
				return report, err
			}
			if err := addTrendCents(&report.SpendThisCents, spend); err != nil {
				return report, err
			}
		} else {
			if err := addTrendCents(&aggregate.category.SpendPrevCents, spend); err != nil {
				return report, err
			}
			if err := addTrendCents(&report.SpendPrevCents, spend); err != nil {
				return report, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read month-over-month trend transactions: %w", err)
	}

	report.DeltaCents, err = subtractTrendCents(report.SpendThisCents, report.SpendPrevCents)
	if err != nil {
		return report, err
	}
	aggregates := make([]trendCategoryAggregate, 0, len(byCategory))
	for _, aggregate := range byCategory {
		aggregate.category.DeltaCents, err = subtractTrendCents(
			aggregate.category.SpendThisCents,
			aggregate.category.SpendPrevCents,
		)
		if err != nil {
			return report, err
		}
		aggregates = append(aggregates, *aggregate)
	}
	sort.Slice(aggregates, func(left, right int) bool {
		leftMagnitude := trendMagnitude(aggregates[left].category.DeltaCents)
		rightMagnitude := trendMagnitude(aggregates[right].category.DeltaCents)
		if leftMagnitude != rightMagnitude {
			return leftMagnitude > rightMagnitude
		}
		if aggregates[left].category.Name != aggregates[right].category.Name {
			return aggregates[left].category.Name < aggregates[right].category.Name
		}
		if aggregates[left].key.valid != aggregates[right].key.valid {
			return !aggregates[left].key.valid
		}
		return aggregates[left].key.id < aggregates[right].key.id
	})

	report.CategoryTotal = len(aggregates)
	shown := aggregates
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	report.Categories = make([]TrendMoMCategory, 0, len(shown))
	for _, aggregate := range shown {
		report.Categories = append(report.Categories, aggregate.category)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit month-over-month trend read: %w", err)
	}
	return report, nil
}

func addTrendCents(total *int64, amount int64) error {
	if (amount > 0 && *total > math.MaxInt64-amount) ||
		(amount < 0 && *total < math.MinInt64-amount) {
		return fmt.Errorf("trend total overflows integer cents")
	}
	*total += amount
	return nil
}

func subtractTrendCents(left, right int64) (int64, error) {
	if (right > 0 && left < math.MinInt64+right) ||
		(right < 0 && left > math.MaxInt64+right) {
		return 0, fmt.Errorf("trend delta overflows integer cents")
	}
	return left - right, nil
}

func trendMagnitude(value int64) uint64 {
	if value < 0 {
		return uint64(-(value + 1)) + 1
	}
	return uint64(value)
}

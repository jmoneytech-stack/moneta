package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/big"
	"sort"
	"time"
)

const (
	anomalyBaselineMonths = 3
	anomalyMinimumDelta   = int64(5000)
)

// AnomalyItem is one category-level spend spike. CategoryID is retained for
// deterministic ordering but is not part of the rendered CLI/API row.
type AnomalyItem struct {
	Category      string
	CategoryID    *int64
	SpendCents    int64
	BaselineCents int64
	DeltaCents    int64
}

// AnomalyReport is the compute-on-read anomaly result for one calendar month
// or current-month-to-date slice.
type AnomalyReport struct {
	Period          string
	SkippedOverflow int
	Items           []AnomalyItem
}

type anomalyDateWindow struct {
	from string
	to   string
}

type anomalyPeriod struct {
	period   string
	selected anomalyDateWindow
	baseline [anomalyBaselineMonths]anomalyDateWindow
}

// ValidateAnomalyPeriod validates asOf and an optional YYYY-MM period. An
// empty period selects the previous complete calendar month.
func ValidateAnomalyPeriod(asOfValue, periodValue string) error {
	_, err := resolveAnomalyPeriod(asOfValue, periodValue)
	return err
}

// ReadAnomalies finds category spend spikes against the mean of the three
// immediately preceding complete months or equal-length MTD slices.
func ReadAnomalies(
	ctx context.Context,
	db *sql.DB,
	asOfValue, periodValue string,
) (AnomalyReport, error) {
	var report AnomalyReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	period, err := resolveAnomalyPeriod(asOfValue, periodValue)
	if err != nil {
		return report, err
	}
	report.Period = period.period

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin anomaly read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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
		WHERE transactions.date >= ?
		  AND transactions.date <= ?
		  AND transactions.status = 'posted'
		  AND transactions.excluded = 0
		  AND transactions.is_transfer = 0
		  AND transactions.amount_cents < 0
		ORDER BY transactions.date, transactions.id
	`, period.baseline[anomalyBaselineMonths-1].from, period.selected.to)
	if err != nil {
		return report, fmt.Errorf("read anomaly transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type anomalyCategoryKey struct {
		valid bool
		id    int64
	}
	type anomalyAggregate struct {
		key      anomalyCategoryKey
		name     string
		selected int64
		baseline [anomalyBaselineMonths]int64
		overflow bool
	}
	byCategory := make(map[anomalyCategoryKey]*anomalyAggregate)
	for rows.Next() {
		var categoryID sql.NullInt64
		var name, date string
		var amount int64
		if err := rows.Scan(&categoryID, &name, &date, &amount); err != nil {
			return report, fmt.Errorf("scan anomaly transaction: %w", err)
		}
		window := anomalyWindowIndex(period, date)
		if window < -1 {
			continue
		}
		key := anomalyCategoryKey{valid: categoryID.Valid, id: categoryID.Int64}
		aggregate := byCategory[key]
		if aggregate == nil {
			aggregate = &anomalyAggregate{key: key, name: name}
			byCategory[key] = aggregate
		}
		if amount == math.MinInt64 {
			aggregate.overflow = true
			continue
		}
		spend := -amount
		if window == -1 {
			if err := addAnomalyCents(&aggregate.selected, spend); err != nil {
				aggregate.overflow = true
			}
			continue
		}
		if err := addAnomalyCents(&aggregate.baseline[window], spend); err != nil {
			aggregate.overflow = true
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read anomaly transactions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("close anomaly transactions: %w", err)
	}

	type orderedAnomaly struct {
		key  anomalyCategoryKey
		item AnomalyItem
	}
	ordered := make([]orderedAnomaly, 0, len(byCategory))
	for _, aggregate := range byCategory {
		if aggregate.overflow {
			report.SkippedOverflow++
			continue
		}
		nonzero := 0
		for _, spend := range aggregate.baseline {
			if spend > 0 {
				nonzero++
			}
		}
		if nonzero < 2 {
			continue
		}
		baseline, err := meanAnomalyCents(aggregate.baseline)
		if err != nil {
			report.SkippedOverflow++
			continue
		}
		if baseline <= 0 {
			continue
		}
		if baseline > math.MaxInt64/2 {
			report.SkippedOverflow++
			continue
		}
		twiceBaseline := baseline * 2
		if aggregate.selected <= twiceBaseline {
			continue
		}
		delta, err := subtractAnomalyCents(aggregate.selected, baseline)
		if err != nil {
			report.SkippedOverflow++
			continue
		}
		if delta < anomalyMinimumDelta {
			continue
		}
		var categoryID *int64
		if aggregate.key.valid {
			value := aggregate.key.id
			categoryID = &value
		}
		ordered = append(ordered, orderedAnomaly{
			key: aggregate.key,
			item: AnomalyItem{
				Category: aggregate.name, CategoryID: categoryID,
				SpendCents: aggregate.selected, BaselineCents: baseline, DeltaCents: delta,
			},
		})
	}
	sort.Slice(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		if left.item.DeltaCents != right.item.DeltaCents {
			return left.item.DeltaCents > right.item.DeltaCents
		}
		if left.item.SpendCents != right.item.SpendCents {
			return left.item.SpendCents > right.item.SpendCents
		}
		if left.item.Category != right.item.Category {
			return left.item.Category < right.item.Category
		}
		if left.key.valid != right.key.valid {
			return !left.key.valid
		}
		return left.key.id < right.key.id
	})
	report.Items = make([]AnomalyItem, 0, len(ordered))
	for _, anomaly := range ordered {
		report.Items = append(report.Items, anomaly.item)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit anomaly read: %w", err)
	}
	return report, nil
}

func resolveAnomalyPeriod(asOfValue, periodValue string) (anomalyPeriod, error) {
	var result anomalyPeriod
	asOf, err := time.Parse(time.DateOnly, asOfValue)
	if err != nil {
		return result, fmt.Errorf("anomaly as-of date must use YYYY-MM-DD: %w", err)
	}
	currentMonth := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	selectedMonth := currentMonth.AddDate(0, -1, 0)
	if periodValue != "" {
		selectedMonth, err = time.Parse("2006-01", periodValue)
		if err != nil || selectedMonth.Format("2006-01") != periodValue {
			return result, fmt.Errorf("period must use valid YYYY-MM form, got %q", periodValue)
		}
		if selectedMonth.After(currentMonth) {
			return result, fmt.Errorf("period %q must not be in the future", periodValue)
		}
	}
	result.period = selectedMonth.Format("2006-01")
	currentMTD := periodValue != "" && selectedMonth.Equal(currentMonth)
	result.selected = anomalyMonthWindow(selectedMonth, currentMTD, asOf.Day())
	for index := range anomalyBaselineMonths {
		month := selectedMonth.AddDate(0, -(index + 1), 0)
		result.baseline[index] = anomalyMonthWindow(month, currentMTD, asOf.Day())
	}
	return result, nil
}

func anomalyMonthWindow(month time.Time, mtd bool, day int) anomalyDateWindow {
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	if mtd {
		to = time.Date(
			month.Year(), month.Month(), min(day, to.Day()),
			0, 0, 0, 0, time.UTC,
		)
	}
	return anomalyDateWindow{from: from.Format(time.DateOnly), to: to.Format(time.DateOnly)}
}

// anomalyWindowIndex returns -1 for the selected period, 0..2 for baseline
// windows, and -2 for dates outside an MTD slice.
func anomalyWindowIndex(period anomalyPeriod, date string) int {
	if date >= period.selected.from && date <= period.selected.to {
		return -1
	}
	for index, window := range period.baseline {
		if date >= window.from && date <= window.to {
			return index
		}
	}
	return -2
}

func addAnomalyCents(total *int64, amount int64) error {
	if amount < 0 || *total > math.MaxInt64-amount {
		return fmt.Errorf("anomaly spend overflows integer cents")
	}
	*total += amount
	return nil
}

func meanAnomalyCents(values [anomalyBaselineMonths]int64) (int64, error) {
	total := new(big.Int)
	for _, value := range values {
		total.Add(total, big.NewInt(value))
	}
	total.Quo(total, big.NewInt(anomalyBaselineMonths))
	if !total.IsInt64() {
		return 0, fmt.Errorf("anomaly baseline overflows integer cents")
	}
	return total.Int64(), nil
}

func subtractAnomalyCents(left, right int64) (int64, error) {
	if right > 0 && left < math.MinInt64+right {
		return 0, fmt.Errorf("anomaly deviation overflows integer cents")
	}
	return left - right, nil
}

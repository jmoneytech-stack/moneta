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

// RecurringFilter selects the read-time date and optional listed kind.
type RecurringFilter struct {
	AsOf string
	Kind string
}

// RecurringKindCounts counts active detected rows only.
type RecurringKindCounts struct {
	Subscription int
	Bill         int
	Income       int
}

// RecurringItem is one recurring list row. DriftPercentX100 holds hundredths
// of one percent as an arbitrary-precision integer for exact output rendering.
type RecurringItem struct {
	Name             string
	Kind             string
	Cadence          string
	ExpectedCents    int64
	NextExpectedDate string
	DriftPercentX100 *big.Int
	Drift            bool
	Active           bool
	Source           string
}

// RecurringReport is the complete read model shared by the CLI and REST API.
type RecurringReport struct {
	Detector                     DetectorState
	ActiveDetected               RecurringKindCounts
	MonthlyEquivalentCents       int64
	MonthlyEquivalentUnconverted int
	Items                        []RecurringItem
}

// ReadRecurring lists recurring rows under the detector freshness gate. It is
// read-only; partial schedule projection changes only the returned date.
func ReadRecurring(
	ctx context.Context,
	db *sql.DB,
	filter RecurringFilter,
) (RecurringReport, error) {
	var report RecurringReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	asOf, err := time.Parse(time.DateOnly, filter.AsOf)
	if err != nil {
		return report, fmt.Errorf("recurring as-of date must use YYYY-MM-DD: %w", err)
	}
	switch filter.Kind {
	case "", "subscription", "bill", "income":
	default:
		return report, fmt.Errorf("recurring kind must be subscription, bill, or income")
	}
	report.Detector, err = ReadDetectorState(ctx, db)
	if err != nil {
		return report, err
	}
	includeDetected := report.Detector.Status == "ok" || report.Detector.Status == "partial"

	rows, err := db.QueryContext(ctx, `
		SELECT
			id,
			name,
			kind,
			cadence,
			expected_cents,
			next_expected_date,
			source,
			is_active,
			last_matched_cents,
			schedule_anchor_day
		FROM recurring_items
		WHERE source = 'manual' OR (? AND source = 'detected')
		ORDER BY id
	`, includeDetected)
	if err != nil {
		return report, fmt.Errorf("read recurring rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type listedItem struct {
		id   int64
		item RecurringItem
	}
	var listed []listedItem
	for rows.Next() {
		var row listedItem
		var nextDate sql.NullString
		var lastMatchedCents, anchor sql.NullInt64
		if err := rows.Scan(
			&row.id,
			&row.item.Name,
			&row.item.Kind,
			&row.item.Cadence,
			&row.item.ExpectedCents,
			&nextDate,
			&row.item.Source,
			&row.item.Active,
			&lastMatchedCents,
			&anchor,
		); err != nil {
			return report, fmt.Errorf("scan recurring row: %w", err)
		}
		if nextDate.Valid {
			row.item.NextExpectedDate = nextDate.String
		}
		row.item.DriftPercentX100, row.item.Drift = recurringDrift(
			row.item.ExpectedCents,
			lastMatchedCents,
		)
		if report.Detector.Status == "partial" && row.item.Source == "detected" &&
			row.item.Active && row.item.NextExpectedDate != "" {
			projected, err := projectRecurringNextDate(
				row.item.NextExpectedDate,
				row.item.Cadence,
				anchor,
				asOf,
			)
			if err != nil {
				return report, fmt.Errorf("project recurring next date: %w", err)
			}
			row.item.NextExpectedDate = projected
		}
		if row.item.Source == "detected" && row.item.Active {
			switch row.item.Kind {
			case "subscription":
				report.ActiveDetected.Subscription++
			case "bill":
				report.ActiveDetected.Bill++
			case "income":
				report.ActiveDetected.Income++
			}
			if row.item.Kind == "subscription" || row.item.Kind == "bill" {
				monthly, converted, err := recurringMonthlyEquivalent(
					row.item.ExpectedCents,
					row.item.Cadence,
				)
				if err != nil {
					return report, err
				}
				if converted {
					report.MonthlyEquivalentCents, err = addRecurringCents(
						report.MonthlyEquivalentCents,
						monthly,
					)
					if err != nil {
						return report, err
					}
				} else {
					report.MonthlyEquivalentUnconverted++
				}
			}
		}
		if filter.Kind == "" || row.item.Kind == filter.Kind {
			listed = append(listed, row)
		}
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read recurring rows: %w", err)
	}

	sort.Slice(listed, func(i, j int) bool {
		left := listed[i].item
		right := listed[j].item
		if left.Active != right.Active {
			return left.Active
		}
		if left.NextExpectedDate == "" || right.NextExpectedDate == "" {
			if left.NextExpectedDate != right.NextExpectedDate {
				return left.NextExpectedDate != ""
			}
		} else if left.NextExpectedDate != right.NextExpectedDate {
			return left.NextExpectedDate < right.NextExpectedDate
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		return listed[i].id < listed[j].id
	})
	report.Items = make([]RecurringItem, 0, len(listed))
	for _, row := range listed {
		report.Items = append(report.Items, row.item)
	}
	return report, nil
}

func recurringDrift(expected int64, latest sql.NullInt64) (*big.Int, bool) {
	if !latest.Valid || expected == 0 {
		return nil, false
	}
	latestMagnitude := new(big.Int).Abs(big.NewInt(latest.Int64))
	expectedMagnitude := new(big.Int).Abs(big.NewInt(expected))
	delta := new(big.Int).Sub(latestMagnitude, expectedMagnitude)
	scaled := new(big.Int).Mul(delta, big.NewInt(10000))
	scaled.Quo(scaled, expectedMagnitude)
	absolute := new(big.Int).Abs(new(big.Int).Set(scaled))
	return scaled, absolute.Cmp(big.NewInt(1000)) > 0
}

func recurringMonthlyEquivalent(expected int64, cadence string) (int64, bool, error) {
	multiplier := int64(1)
	divisor := int64(1)
	switch cadence {
	case "weekly":
		multiplier, divisor = 52, 12
	case "biweekly":
		multiplier, divisor = 26, 12
	case "monthly":
	case "quarterly":
		divisor = 3
	default:
		return 0, false, nil
	}
	if multiplier != 1 {
		if expected > 0 && expected > math.MaxInt64/multiplier ||
			expected < 0 && expected < math.MinInt64/multiplier {
			return 0, false, fmt.Errorf("recurring monthly equivalent overflows integer cents")
		}
		expected *= multiplier
	}
	return expected / divisor, true, nil
}

func addRecurringCents(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right ||
		right < 0 && left < math.MinInt64-right {
		return 0, fmt.Errorf("recurring monthly equivalent total overflows integer cents")
	}
	return left + right, nil
}

func projectRecurringNextDate(
	stored string,
	cadence string,
	anchor sql.NullInt64,
	asOf time.Time,
) (string, error) {
	next, err := time.Parse(time.DateOnly, stored)
	if err != nil {
		return "", err
	}
	graceDays := 0
	stepDays := 0
	stepMonths := 0
	switch cadence {
	case "weekly":
		graceDays, stepDays = 1, 7
	case "biweekly":
		graceDays, stepDays = 1, 14
	case "monthly":
		graceDays, stepMonths = 3, 1
	case "quarterly":
		graceDays, stepMonths = 3, 3
	default:
		return stored, nil
	}
	anchorDay := next.Day()
	if anchor.Valid && anchor.Int64 >= 1 && anchor.Int64 <= 31 {
		anchorDay = int(anchor.Int64)
	}
	for asOf.After(next.AddDate(0, 0, graceDays)) {
		if stepDays != 0 {
			next = next.AddDate(0, 0, stepDays)
			continue
		}
		targetMonth := time.Date(
			next.Year(), next.Month()+time.Month(stepMonths), 1,
			0, 0, 0, 0, time.UTC,
		)
		day := min(anchorDay, recurringDaysInMonth(targetMonth.Year(), targetMonth.Month()))
		next = time.Date(targetMonth.Year(), targetMonth.Month(), day, 0, 0, 0, 0, time.UTC)
	}
	return next.Format(time.DateOnly), nil
}

func recurringDaysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

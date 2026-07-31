package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

const cardGraceDays = 3

// BillItem is one unified recurring obligation or active credit-card due row.
type BillItem struct {
	Date        string
	Name        string
	AmountCents *int64
	Source      string
	Kind        string
	DateSource  string
	DueStatus   string
}

// BillsReport is the shared read model for the CLI, REST API, and dashboard.
type BillsReport struct {
	AsOf     string
	Through  string
	Days     int
	Detector DetectorState
	Items    []BillItem
}

// ReadBills merges active detected outflow schedules and active credit-card
// dues for the inclusive horizon from asOf through asOf+days calendar days.
func ReadBills(
	ctx context.Context,
	db *sql.DB,
	asOfValue string,
	days int,
) (BillsReport, error) {
	report := BillsReport{AsOf: asOfValue, Days: days}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	asOf, err := time.Parse(time.DateOnly, asOfValue)
	if err != nil {
		return report, fmt.Errorf("bills as-of date must use YYYY-MM-DD: %w", err)
	}
	if days < 1 || days > 366 {
		return report, fmt.Errorf("bills days must be from 1 to 366")
	}
	end := asOf.AddDate(0, 0, days)
	report.Through = end.Format(time.DateOnly)
	report.Detector, err = ReadDetectorState(ctx, db)
	if err != nil {
		return report, err
	}

	if report.Detector.Status == "ok" || report.Detector.Status == "partial" {
		recurringItems, err := readRecurringBills(ctx, db, report.Detector.Status, asOf, end)
		if err != nil {
			return report, err
		}
		report.Items = append(report.Items, recurringItems...)
	}
	cardItems, err := readCardBills(ctx, db, asOf, end)
	if err != nil {
		return report, err
	}
	report.Items = append(report.Items, cardItems...)

	sort.Slice(report.Items, func(i, j int) bool {
		left := report.Items[i]
		right := report.Items[j]
		if left.Date != right.Date {
			return left.Date < right.Date
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		return left.Kind < right.Kind
	})
	return report, nil
}

func readRecurringBills(
	ctx context.Context,
	db *sql.DB,
	detectorStatus string,
	asOf, end time.Time,
) ([]BillItem, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, kind, cadence, expected_cents, next_expected_date,
			schedule_anchor_day
		FROM recurring_items
		WHERE source = 'detected'
		  AND kind IN ('subscription', 'bill')
		  AND is_active = 1
		  AND expected_cents < 0
		  AND next_expected_date IS NOT NULL
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("read recurring bills: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []BillItem
	for rows.Next() {
		var name, kind, cadence, storedDate string
		var expected int64
		var anchor sql.NullInt64
		if err := rows.Scan(&name, &kind, &cadence, &expected, &storedDate, &anchor); err != nil {
			return nil, fmt.Errorf("scan recurring bill: %w", err)
		}
		graceDays, knownCadence := recurringGraceDays(cadence)
		if !knownCadence {
			return nil, fmt.Errorf("recurring bill %q has unsupported cadence %q", name, cadence)
		}
		effectiveDate := storedDate
		if detectorStatus == "partial" {
			projected, err := projectRecurringNextDate(storedDate, cadence, anchor, asOf)
			if err != nil {
				return nil, fmt.Errorf("project recurring bill %q: %w", name, err)
			}
			effectiveDate = projected
		}
		dueDate, err := time.Parse(time.DateOnly, effectiveDate)
		if err != nil {
			return nil, fmt.Errorf("parse recurring bill date %q: %w", effectiveDate, err)
		}
		if dueDate.After(end) || asOf.After(dueDate.AddDate(0, 0, graceDays)) {
			continue
		}
		amount, err := checkedBillMagnitude(expected)
		if err != nil {
			return nil, fmt.Errorf("recurring bill %q amount: %w", name, err)
		}
		items = append(items, BillItem{
			Date:        effectiveDate,
			Name:        name,
			AmountCents: &amount,
			Source:      "recurring",
			Kind:        kind,
			DateSource:  "detected_schedule",
			DueStatus:   billDueStatus(dueDate, asOf),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recurring bills: %w", err)
	}
	return items, nil
}

func readCardBills(
	ctx context.Context,
	db *sql.DB,
	asOf, end time.Time,
) ([]BillItem, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT accounts.name, credit_terms.min_payment_cents,
			credit_terms.due_day, credit_terms.next_payment_due_date
		FROM accounts
		LEFT JOIN credit_terms ON credit_terms.account_id = accounts.id
		WHERE accounts.type = 'credit_card' AND accounts.is_active = 1
		ORDER BY accounts.id
	`)
	if err != nil {
		return nil, fmt.Errorf("read card bills: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []BillItem
	for rows.Next() {
		var name string
		var minimum, dueDay sql.NullInt64
		var providerDate sql.NullString
		if err := rows.Scan(&name, &minimum, &dueDay, &providerDate); err != nil {
			return nil, fmt.Errorf("scan card bill: %w", err)
		}
		date, dateSource, found, err := selectCardBillDate(asOf, providerDate, dueDay)
		if err != nil {
			return nil, fmt.Errorf("select card bill date for %q: %w", name, err)
		}
		if !found || date.After(end) || asOf.After(date.AddDate(0, 0, cardGraceDays)) {
			continue
		}
		var amount *int64
		if minimum.Valid {
			if minimum.Int64 < 0 {
				return nil, fmt.Errorf("card bill %q has negative minimum payment", name)
			}
			value := minimum.Int64
			amount = &value
		}
		items = append(items, BillItem{
			Date:        date.Format(time.DateOnly),
			Name:        name,
			AmountCents: amount,
			Source:      "card_due",
			Kind:        "bill",
			DateSource:  dateSource,
			DueStatus:   billDueStatus(date, asOf),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read card bills: %w", err)
	}
	return items, nil
}

func selectCardBillDate(
	asOf time.Time,
	providerDate sql.NullString,
	dueDay sql.NullInt64,
) (time.Time, string, bool, error) {
	if providerDate.Valid {
		date, err := time.Parse(time.DateOnly, providerDate.String)
		if err != nil {
			return time.Time{}, "", false, err
		}
		if !asOf.After(date.AddDate(0, 0, cardGraceDays)) {
			return date, "provider_reported", true, nil
		}
		projected, err := projectRecurringNextDate(
			providerDate.String,
			"monthly",
			sql.NullInt64{Int64: int64(date.Day()), Valid: true},
			asOf,
		)
		if err != nil {
			return time.Time{}, "", false, err
		}
		date, err = time.Parse(time.DateOnly, projected)
		return date, "projected_from_past_provider_date", true, err
	}
	if !dueDay.Valid {
		return time.Time{}, "", false, nil
	}
	if dueDay.Int64 < 1 || dueDay.Int64 > 31 {
		return time.Time{}, "", false, fmt.Errorf("due day must be from 1 to 31")
	}
	day := min(int(dueDay.Int64), recurringDaysInMonth(asOf.Year(), asOf.Month()))
	candidate := time.Date(asOf.Year(), asOf.Month(), day, 0, 0, 0, 0, time.UTC)
	projected, err := projectRecurringNextDate(
		candidate.Format(time.DateOnly),
		"monthly",
		sql.NullInt64{Int64: dueDay.Int64, Valid: true},
		asOf,
	)
	if err != nil {
		return time.Time{}, "", false, err
	}
	date, err := time.Parse(time.DateOnly, projected)
	return date, "day_of_month_estimate", true, err
}

func checkedBillMagnitude(value int64) (int64, error) {
	if value == math.MinInt64 {
		return 0, fmt.Errorf("magnitude overflows integer cents")
	}
	if value < 0 {
		return -value, nil
	}
	return value, nil
}

func billDueStatus(date, asOf time.Time) string {
	if date.After(asOf) {
		return "upcoming"
	}
	if date.Equal(asOf) {
		return "due"
	}
	return "in_grace"
}

package report

import (
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// Recurring builds the shared moneta recurring and /v1/recurring document.
func Recurring(report store.RecurringReport) toon.Object {
	table := toon.Table{
		Fields: []string{
			"name", "kind", "cadence", "expected", "next_expected_date",
			"drift_pct", "drift", "active", "source",
		},
		Rows: make([][]any, 0, len(report.Items)),
	}
	for _, item := range report.Items {
		nextDate := any(nil)
		if item.NextExpectedDate != "" {
			nextDate = item.NextExpectedDate
		}
		driftPercent := any(nil)
		if item.DriftPercentX100 != nil {
			driftPercent = cli.ScaledBigInteger(item.DriftPercentX100, 2)
		}
		table.Rows = append(table.Rows, []any{
			item.Name,
			item.Kind,
			item.Cadence,
			cli.Money(item.ExpectedCents),
			nextDate,
			driftPercent,
			item.Drift,
			item.Active,
			item.Source,
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: len(report.Items)},
			{Key: "detector", Value: Detector(report.Detector, false)},
			{Key: "by_kind", Value: toon.Object{
				{Key: "subscription", Value: report.ActiveDetected.Subscription},
				{Key: "bill", Value: report.ActiveDetected.Bill},
				{Key: "income", Value: report.ActiveDetected.Income},
			}},
			{Key: "monthly_equivalent", Value: cli.Money(report.MonthlyEquivalentCents)},
			{Key: "monthly_equivalent_unconverted", Value: report.MonthlyEquivalentUnconverted},
		}},
		{Key: "recurring", Value: table},
		{Key: "hint", Value: recurringHint(report)},
	}
}

func recurringHint(report store.RecurringReport) string {
	if report.Detector.Status == "never_run" {
		return "run moneta sync to detect recurring transactions"
	}
	if report.Detector.Status == "error" {
		return "recurring detection failed; run moneta status, then retry moneta sync"
	}
	if len(report.Items) == 0 {
		return "no recurring series known; run moneta sync to refresh detection"
	}
	if report.Detector.Status == "partial" {
		return "recurring data is partial; run an unscoped moneta sync when all items are available"
	}
	return "run moneta sync to refresh recurring detection"
}

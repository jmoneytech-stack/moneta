package report

import (
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

const dashboardBillsLimit = 5

// Bills builds the shared moneta bills and /v1/bills document.
func Bills(report store.BillsReport) toon.Object {
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "as_of", Value: report.AsOf},
			{Key: "through", Value: report.Through},
			{Key: "days", Value: report.Days},
			{Key: "count", Value: len(report.Items)},
			{Key: "detector", Value: Detector(report.Detector, false)},
		}},
		{Key: "bills", Value: billsTable(report.Items, len(report.Items))},
		{Key: "hint", Value: billsHint(report)},
	}
}

func billsTable(items []store.BillItem, limit int) toon.Table {
	if limit > len(items) {
		limit = len(items)
	}
	table := toon.Table{
		Fields: []string{"date", "name", "amount", "source", "kind", "date_source", "due_status"},
		Rows:   make([][]any, 0, limit),
	}
	for _, item := range items[:limit] {
		amount := any(nil)
		if item.AmountCents != nil {
			amount = cli.Money(*item.AmountCents)
		}
		table.Rows = append(table.Rows, []any{
			item.Date,
			item.Name,
			amount,
			item.Source,
			item.Kind,
			item.DateSource,
			item.DueStatus,
		})
	}
	return table
}

func billsHint(report store.BillsReport) string {
	if report.Detector.Status == "never_run" {
		return "run moneta sync to detect recurring bills; card dues are still included"
	}
	if report.Detector.Status == "error" {
		return "recurring detection failed; card dues are still included, and moneta status has details"
	}
	if report.Detector.Status == "partial" {
		return "recurring bill data is partial; run an unscoped moneta sync when all items are available"
	}
	if len(report.Items) == 0 {
		return "no bills are due in this horizon"
	}
	return "run moneta bills --days N to change the calendar horizon"
}

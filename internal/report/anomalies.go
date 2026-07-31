package report

import (
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

const dashboardAnomaliesLimit = 3

// Anomalies builds the shared moneta anomalies and /v1/anomalies document.
func Anomalies(report store.AnomalyReport) toon.Object {
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "period", Value: report.Period},
			{Key: "count", Value: len(report.Items)},
			{Key: "skipped_overflow", Value: report.SkippedOverflow},
		}},
		{Key: "anomalies", Value: anomaliesTable(report.Items, len(report.Items))},
		{Key: "hint", Value: anomaliesHint(report)},
	}
}

func anomaliesTable(items []store.AnomalyItem, limit int) toon.Table {
	if limit > len(items) {
		limit = len(items)
	}
	table := toon.Table{
		Fields: []string{"category", "spend", "baseline", "deviation_ratio"},
		Rows:   make([][]any, 0, limit),
	}
	for _, item := range items[:limit] {
		deviation := any(nil)
		if ratio := cli.Ratio(item.DeltaCents, item.BaselineCents, 4); ratio != nil {
			deviation = *ratio
		}
		table.Rows = append(table.Rows, []any{
			item.Category,
			cli.Money(item.SpendCents),
			cli.Money(item.BaselineCents),
			deviation,
		})
	}
	return table
}

func anomaliesHint(report store.AnomalyReport) string {
	if report.SkippedOverflow > 0 {
		return "one or more categories exceeded safe integer math and were skipped"
	}
	if len(report.Items) == 0 {
		return "no category spend spikes crossed both anomaly thresholds"
	}
	return "run moneta spend --period " + report.Period + " for the category breakdown"
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

const trendMetricMoM = "mom"

// runTrends dispatches one compute-on-read trend metric. PR4 supports only
// month-over-month category spend; later metrics extend the dispatch without
// changing the command's output and error boundaries.
func runTrends(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runTrendsAt(ctx, args, stdout, stderr, time.Now())
}

func runTrendsAt(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("trends", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	metric := flags.String("metric", "", "trend metric (required; supported: mom)")
	periodValue := flags.String(
		"period",
		"",
		"current comparison month in YYYY-MM form (default: current local month)",
	)
	from := flags.String("from", "", "custom start date (not supported by metric mom)")
	to := flags.String("to", "", "custom end date (not supported by metric mom)")
	account := flags.String(
		"account",
		"",
		"filter to accounts whose name contains this literal substring (case-insensitive)",
	)
	limit := flags.Int("limit", statusDefaultLimit, "maximum category rows to show")
	full := flags.Bool("full", false, "show every category row, ignoring --limit")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: trends does not accept positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
		return 2
	}
	if *metric == "" {
		fmt.Fprintln(stderr, "error: --metric is required (supported: mom)")
		return 2
	}
	if *metric != trendMetricMoM {
		fmt.Fprintf(stderr, "error: unknown --metric %q (supported: mom)\n", *metric)
		return 2
	}
	if *from != "" || *to != "" {
		fmt.Fprintln(
			stderr,
			"error: --metric mom requires --period YYYY-MM or the default current month; --from/--to are unsupported",
		)
		return 2
	}
	period, err := store.ResolveTrendMoMPeriod(*periodValue, now)
	if err != nil {
		fmt.Fprintf(stderr, "error: --period %v\n", err)
		return 2
	}
	if *databasePath == "" {
		fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}

	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	rowLimit := 0
	if !*full {
		rowLimit = *limit
	}
	filter := store.TrendMoMFilter{
		ThisFrom: period.ThisFrom,
		ThisTo:   period.ThisTo,
		PrevFrom: period.PrevFrom,
		PrevTo:   period.PrevTo,
		Account:  *account,
	}

	var document toon.Object
	switch *metric {
	case trendMetricMoM:
		report, err := store.ReadTrendMoM(ctx, database, filter, rowLimit)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		document = buildTrendMoMDoc(report, filter)
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, document, format); err != nil {
		fmt.Fprintf(stderr, "error: render trends: %v\n", err)
		return 1
	}
	return 0
}

func buildTrendMoMDoc(report store.TrendMoMReport, filter store.TrendMoMFilter) toon.Object {
	categories := toon.Table{
		Fields: []string{"category", "spend_this", "spend_prev", "delta"},
		Rows:   make([][]any, 0, len(report.Categories)),
	}
	for _, category := range report.Categories {
		categories.Rows = append(categories.Rows, []any{
			category.Name,
			cli.Money(category.SpendThisCents),
			cli.Money(category.SpendPrevCents),
			cli.Money(category.DeltaCents),
		})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "metric", Value: trendMetricMoM},
			{Key: "this_from", Value: filter.ThisFrom},
			{Key: "this_to", Value: filter.ThisTo},
			{Key: "prev_from", Value: filter.PrevFrom},
			{Key: "prev_to", Value: filter.PrevTo},
			{Key: "spend_this", Value: cli.Money(report.SpendThisCents)},
			{Key: "spend_prev", Value: cli.Money(report.SpendPrevCents)},
			{Key: "delta", Value: cli.Money(report.DeltaCents)},
			{Key: "categories", Value: report.CategoryTotal},
		}},
		{Key: "by_category", Value: categories},
	}
	if len(report.Categories) < report.CategoryTotal {
		document = append(document, toon.Field{
			Key: "truncated",
			Value: fmt.Sprintf(
				"%d of %d categories shown (--full for all)",
				len(report.Categories),
				report.CategoryTotal,
			),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: trendMoMHint(report, filter)})
}

func trendMoMHint(report store.TrendMoMReport, filter store.TrendMoMFilter) string {
	if report.CategoryTotal == 0 {
		return "no posted spending in either month; choose another --period or run moneta sync"
	}
	return fmt.Sprintf(
		"run moneta spend --period %s to inspect current-month spending",
		filter.ThisFrom[:7],
	)
}

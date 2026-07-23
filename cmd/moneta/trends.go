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

const (
	trendMetricMoM         = "mom"
	trendMetricMerchants   = "merchants"
	trendMetricUtilization = "utilization"
	trendMetricsHelp       = "mom, merchants, utilization"
)

// runTrends dispatches one compute-on-read trend metric. Each metric owns its
// period validation while sharing the command's output and error boundaries.
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
	metric := flags.String("metric", "", "trend metric (required; supported: "+trendMetricsHelp+")")
	periodValue := flags.String(
		"period",
		"",
		"calendar month in YYYY-MM form (default: current local month)",
	)
	history := flags.String("history", "", "daily history window in Nd form (utilization only; default 30d)")
	from := flags.String("from", "", "custom start date for supported metrics, YYYY-MM-DD")
	to := flags.String("to", "", "custom end date for supported metrics, YYYY-MM-DD")
	account := flags.String(
		"account",
		"",
		"filter to accounts whose name contains this literal substring (case-insensitive)",
	)
	limit := flags.Int("limit", statusDefaultLimit, "maximum result rows to show")
	full := flags.Bool("full", false, "show every result row, ignoring --limit")
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
	provided := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) {
		provided[value.Name] = true
	})
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
		return 2
	}
	if *metric == "" {
		fmt.Fprintf(stderr, "error: --metric is required (supported: %s)\n", trendMetricsHelp)
		return 2
	}
	if *metric != trendMetricMoM && *metric != trendMetricMerchants &&
		*metric != trendMetricUtilization {
		fmt.Fprintf(
			stderr,
			"error: unknown --metric %q (supported: %s)\n",
			*metric,
			trendMetricsHelp,
		)
		return 2
	}

	var momFilter store.TrendMoMFilter
	var merchantsFilter store.TrendMerchantsFilter
	var utilizationFilter store.TrendUtilizationFilter
	switch *metric {
	case trendMetricMoM:
		if provided["history"] {
			fmt.Fprintln(stderr, "error: --history is supported only by --metric utilization")
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
		momFilter = store.TrendMoMFilter{
			ThisFrom: period.ThisFrom,
			ThisTo:   period.ThisTo,
			PrevFrom: period.PrevFrom,
			PrevTo:   period.PrevTo,
			Account:  *account,
		}
	case trendMetricMerchants:
		if provided["history"] {
			fmt.Fprintln(stderr, "error: --history is supported only by --metric utilization")
			return 2
		}
		period, err := resolveReadPeriod(*periodValue, *from, *to, now)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
		merchantsFilter = store.TrendMerchantsFilter{
			From: period.From, To: period.To, Account: *account,
		}
	case trendMetricUtilization:
		if provided["limit"] || provided["full"] {
			fmt.Fprintln(stderr, "error: --limit/--full are unsupported by --metric utilization")
			return 2
		}
		period, err := resolveTrendUtilizationPeriod(
			*history,
			provided["history"],
			*periodValue,
			provided["period"],
			*from,
			provided["from"],
			*to,
			provided["to"],
			now,
		)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
		utilizationFilter = store.TrendUtilizationFilter{
			From: period.From, To: period.To, Account: *account,
		}
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
	var document toon.Object
	switch *metric {
	case trendMetricMoM:
		report, err := store.ReadTrendMoM(ctx, database, momFilter, rowLimit)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		document = buildTrendMoMDoc(report, momFilter)
	case trendMetricMerchants:
		report, err := store.ReadTrendMerchants(ctx, database, merchantsFilter, rowLimit)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		document = buildTrendMerchantsDoc(report, merchantsFilter)
	case trendMetricUtilization:
		report, err := store.ReadTrendUtilization(ctx, database, utilizationFilter)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		document = buildTrendUtilizationDoc(report)
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

func resolveTrendUtilizationPeriod(
	history string,
	historyProvided bool,
	periodValue string,
	periodProvided bool,
	from string,
	fromProvided bool,
	to string,
	toProvided bool,
	now time.Time,
) (readPeriod, error) {
	if historyProvided && (periodProvided || fromProvided || toProvided) {
		return readPeriod{}, fmt.Errorf("--history cannot be combined with --period, --from, or --to")
	}
	if historyProvided {
		window, err := store.ResolveNetworthHistoryWindow(history, now)
		if err != nil {
			return readPeriod{}, fmt.Errorf("--history %v", err)
		}
		return readPeriod{From: window.From, To: window.To}, nil
	}
	if periodProvided || fromProvided || toProvided {
		return resolveReadPeriod(periodValue, from, to, now)
	}
	window, err := store.ResolveNetworthHistoryWindow("30d", now)
	if err != nil {
		return readPeriod{}, err
	}
	return readPeriod{From: window.From, To: window.To}, nil
}

func buildTrendUtilizationDoc(report store.TrendUtilizationReport) toon.Object {
	history := toon.Table{
		Fields: []string{"date", "utilization", "debt", "limit", "accounts"},
		Rows:   make([][]any, 0, len(report.Points)),
	}
	for _, point := range report.Points {
		utilization := any(nil)
		if point.HasUtilization {
			if value := cli.Ratio(point.DebtCents, point.LimitCents, 4); value != nil {
				utilization = *value
			}
		}
		history.Rows = append(history.Rows, []any{
			point.Date,
			utilization,
			cli.Money(point.DebtCents),
			cli.Money(point.LimitCents),
			point.Accounts,
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "metric", Value: trendMetricUtilization},
			{Key: "from", Value: report.From},
			{Key: "to", Value: report.To},
			{Key: "days", Value: report.Days},
			{Key: "accounts", Value: report.Accounts},
			{Key: "missing_limit_days", Value: report.MissingLimitDays},
		}},
		{Key: "history", Value: history},
		{Key: "hint", Value: trendUtilizationHint(report)},
	}
}

func trendUtilizationHint(report store.TrendUtilizationReport) string {
	if report.Accounts == 0 {
		return "no credit-card accounts match this window; run moneta sync or relax --account"
	}
	hasUtilization := false
	for _, point := range report.Points {
		if point.HasUtilization {
			hasUtilization = true
			break
		}
	}
	if !hasUtilization {
		if report.MissingLimitDays > 0 {
			return "no usable positive credit limits; run moneta sync to refresh card limits"
		}
		return "no credit-card balance snapshots on or before this window; run moneta sync"
	}
	if report.MissingLimitDays > 0 {
		return "some card-days exclude missing or non-positive limits; run moneta sync"
	}
	return "run moneta debts to inspect current card balances and limits"
}

func buildTrendMerchantsDoc(
	report store.TrendMerchantsReport,
	filter store.TrendMerchantsFilter,
) toon.Object {
	merchants := toon.Table{
		Fields: []string{"merchant", "spend", "count"},
		Rows:   make([][]any, 0, len(report.Merchants)),
	}
	for _, merchant := range report.Merchants {
		merchants.Rows = append(merchants.Rows, []any{
			merchant.Name,
			cli.Money(merchant.SpendCents),
			merchant.Count,
		})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "metric", Value: trendMetricMerchants},
			{Key: "from", Value: filter.From},
			{Key: "to", Value: filter.To},
			{Key: "spend", Value: cli.Money(report.SpendCents)},
			{Key: "count", Value: report.Count},
			{Key: "merchants", Value: report.MerchantTotal},
		}},
		{Key: "by_merchant", Value: merchants},
	}
	if len(report.Merchants) < report.MerchantTotal {
		document = append(document, toon.Field{
			Key: "truncated",
			Value: fmt.Sprintf(
				"%d of %d merchants shown (--full for all)",
				len(report.Merchants),
				report.MerchantTotal,
			),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: trendMerchantsHint(report, filter)})
}

func trendMerchantsHint(
	report store.TrendMerchantsReport,
	filter store.TrendMerchantsFilter,
) string {
	if report.Count == 0 {
		return "no posted spending in this period; widen --period/--from/--to or run moneta sync"
	}
	return fmt.Sprintf(
		"run moneta tx --from %s --to %s to inspect merchant transactions",
		filter.From,
		filter.To,
	)
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

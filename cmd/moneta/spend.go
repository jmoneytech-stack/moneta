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

// runSpend prints posted, non-excluded outflows for one period as positive
// spend. It is read-only against the local database. Exit codes: 0 ok, 1
// runtime error, 2 usage.
func runSpend(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("spend", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	periodValue := flags.String(
		"period",
		"",
		"calendar month in YYYY-MM form (default: current local month)",
	)
	from := flags.String("from", "", "custom period start, YYYY-MM-DD (inclusive; requires --to)")
	to := flags.String("to", "", "custom period end, YYYY-MM-DD (inclusive; requires --from)")
	account := flags.String(
		"account",
		"",
		"filter to accounts whose name contains this literal substring (case-insensitive)",
	)
	limit := flags.Int("limit", statusDefaultLimit, "maximum rows per breakdown table")
	full := flags.Bool("full", false, "show every breakdown row, ignoring --limit")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: spend does not accept positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
		return 2
	}
	period, err := resolveReadPeriod(*periodValue, *from, *to, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
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
	filter := store.SpendFilter{
		From:    period.From,
		To:      period.To,
		Account: *account,
	}
	report, err := store.ReadSpend(ctx, database, filter, rowLimit)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildSpendDoc(report, filter), format); err != nil {
		fmt.Fprintf(stderr, "error: render spend: %v\n", err)
		return 1
	}
	return 0
}

func buildSpendDoc(report store.SpendReport, filter store.SpendFilter) toon.Object {
	categories := toon.Table{
		Fields: []string{"category", "spend", "count"},
		Rows:   make([][]any, 0, len(report.Categories)),
	}
	for _, group := range report.Categories {
		categories.Rows = append(categories.Rows, []any{
			group.Name,
			cli.Money(group.SpendCents),
			group.Count,
		})
	}
	merchants := toon.Table{
		Fields: []string{"merchant", "spend", "count"},
		Rows:   make([][]any, 0, len(report.Merchants)),
	}
	for _, group := range report.Merchants {
		merchants.Rows = append(merchants.Rows, []any{
			group.Name,
			cli.Money(group.SpendCents),
			group.Count,
		})
	}

	doc := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "from", Value: filter.From},
			{Key: "to", Value: filter.To},
			{Key: "count", Value: report.Summary.Count},
			{Key: "total_spend", Value: cli.Money(report.Summary.SpendCents)},
		}},
		{Key: "by_category", Value: categories},
	}
	if len(report.Categories) < report.CategoryTotal {
		doc = append(doc, toon.Field{
			Key: "category_truncated",
			Value: fmt.Sprintf(
				"%d of %d groups shown (--full for all)",
				len(report.Categories),
				report.CategoryTotal,
			),
		})
	}
	doc = append(doc, toon.Field{Key: "by_merchant", Value: merchants})
	if len(report.Merchants) < report.MerchantTotal {
		doc = append(doc, toon.Field{
			Key: "merchant_truncated",
			Value: fmt.Sprintf(
				"%d of %d groups shown (--full for all)",
				len(report.Merchants),
				report.MerchantTotal,
			),
		})
	}
	doc = append(doc, toon.Field{Key: "hint", Value: spendHint(report.Summary.Count, filter)})
	return doc
}

func spendHint(count int, filter store.SpendFilter) string {
	if count == 0 {
		return "no posted spending in this period; widen --period/--from/--to or run moneta sync"
	}
	return fmt.Sprintf(
		"run moneta tx --from %s --to %s to inspect ledger rows",
		filter.From,
		filter.To,
	)
}

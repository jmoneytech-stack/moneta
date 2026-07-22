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

// runCashflow prints posted, non-excluded inflow, outflow, net, and savings
// rate for one period. It is read-only against the local database. Exit
// codes: 0 ok, 1 runtime error, 2 usage.
func runCashflow(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cashflow", flag.ContinueOnError)
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
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: cashflow does not accept positional arguments")
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

	filter := store.CashflowFilter{
		From:    period.From,
		To:      period.To,
		Account: *account,
	}
	summary, err := store.ReadCashflow(ctx, database, filter)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildCashflowDoc(summary, filter), format); err != nil {
		fmt.Fprintf(stderr, "error: render cashflow: %v\n", err)
		return 1
	}
	return 0
}

func buildCashflowDoc(summary store.CashflowSummary, filter store.CashflowFilter) toon.Object {
	rate := any(nil)
	if value := savingsRateNumber(summary.NetCents, summary.InflowCents); value != nil {
		rate = *value
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "from", Value: filter.From},
			{Key: "to", Value: filter.To},
			{Key: "count", Value: summary.Count},
			{Key: "inflow", Value: cli.Money(summary.InflowCents)},
			{Key: "outflow", Value: cli.Money(summary.OutflowCents)},
			{Key: "net", Value: cli.Money(summary.NetCents)},
			{Key: "savings_rate", Value: rate},
		}},
		{Key: "hint", Value: cashflowHint(summary, filter)},
	}
}

// savingsRateNumber keeps the cashflow test and builder contract while
// delegating all scaled-decimal formatting to the shared CLI helper.
func savingsRateNumber(netCents, inflowCents int64) *toon.Number {
	return cli.Ratio(netCents, inflowCents, 4)
}

func cashflowHint(summary store.CashflowSummary, filter store.CashflowFilter) string {
	if summary.Count == 0 {
		return "no posted cashflow in this period; widen --period/--from/--to or run moneta sync"
	}
	if summary.InflowCents == 0 {
		return "savings_rate is null because inflow is zero; widen the period or inspect moneta tx"
	}
	return fmt.Sprintf(
		"run moneta spend --from %s --to %s to inspect outflows",
		filter.From,
		filter.To,
	)
}

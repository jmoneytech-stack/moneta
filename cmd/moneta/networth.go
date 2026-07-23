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

// runNetworth prints the latest balance snapshot per account, optionally at
// or before one date, or an inclusive daily history ending today. It is
// read-only against the local database. Exit codes: 0 ok, 1 runtime error,
// 2 usage.
func runNetworth(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runNetworthAt(ctx, args, stdout, stderr, time.Now())
}

func runNetworthAt(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("networth", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	asOf := flags.String(
		"as-of",
		"",
		"latest balance on or before YYYY-MM-DD (default: latest available)",
	)
	history := flags.String(
		"history",
		"",
		"daily history for Nd local-calendar days ending today, inclusive (maximum 3660d)",
	)
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: networth does not accept positional arguments")
		return 2
	}
	historyProvided := false
	flags.Visit(func(selected *flag.Flag) {
		if selected.Name == "history" {
			historyProvided = true
		}
	})
	if historyProvided && *asOf != "" {
		fmt.Fprintln(stderr, "error: --history cannot be combined with --as-of")
		return 2
	}
	if err := validateCLIDate("as-of", *asOf); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	var historyFilter store.NetworthHistoryFilter
	if historyProvided {
		var err error
		historyFilter, err = store.ResolveNetworthHistoryWindow(*history, now)
		if err != nil {
			fmt.Fprintf(stderr, "error: --history %v\n", err)
			return 2
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

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if historyProvided {
		report, err := store.ReadNetworthHistory(ctx, database, historyFilter)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if err := cli.Render(stdout, buildNetworthHistoryDoc(report), format); err != nil {
			fmt.Fprintf(stderr, "error: render networth history: %v\n", err)
			return 1
		}
		return 0
	}

	filter := store.NetworthFilter{AsOf: *asOf}
	report, err := store.ReadNetworth(ctx, database, filter)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := cli.Render(stdout, buildNetworthDoc(report, filter), format); err != nil {
		fmt.Fprintf(stderr, "error: render networth: %v\n", err)
		return 1
	}
	return 0
}

func buildNetworthHistoryDoc(report store.NetworthHistoryReport) toon.Object {
	history := toon.Table{
		Fields: []string{"date", "assets", "liabilities", "networth"},
		Rows:   make([][]any, 0, len(report.Points)),
	}
	for _, point := range report.Points {
		history.Rows = append(history.Rows, []any{
			point.Date,
			cli.Money(point.AssetsCents),
			cli.Money(point.LiabilitiesCents),
			cli.Money(point.NetworthCents),
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "from", Value: report.From},
			{Key: "to", Value: report.To},
			{Key: "days", Value: report.Days},
		}},
		{Key: "history", Value: history},
		{Key: "hint", Value: networthHistoryHint(report)},
	}
}

func networthHistoryHint(report store.NetworthHistoryReport) string {
	if !report.HasBalances {
		return fmt.Sprintf(
			"no balance snapshots on or before %s; run moneta sync or choose a later history window",
			report.To,
		)
	}
	return fmt.Sprintf(
		"run moneta networth --as-of %s for account-type detail",
		report.To,
	)
}

func buildNetworthDoc(report store.NetworthReport, filter store.NetworthFilter) toon.Object {
	asOf := any(nil)
	if report.AsOf != "" {
		asOf = report.AsOf
	}
	byType := toon.Table{
		Fields: []string{"type", "count", "balance"},
		Rows:   make([][]any, 0, len(report.ByType)),
	}
	for _, group := range report.ByType {
		balance := any(nil)
		if group.BalancedCount > 0 {
			balance = cli.Money(group.BalanceCents)
		}
		byType.Rows = append(byType.Rows, []any{group.Type, group.Count, balance})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "as_of", Value: asOf},
			{Key: "assets", Value: cli.Money(report.AssetsCents)},
			{Key: "liabilities", Value: cli.Money(report.LiabilitiesCents)},
			{Key: "networth", Value: cli.Money(report.NetworthCents)},
			{Key: "accounts", Value: report.Accounts},
			{Key: "missing_balance", Value: report.MissingBalance},
		}},
		{Key: "by_type", Value: byType},
		{Key: "hint", Value: networthHint(report, filter)},
	}
}

func networthHint(report store.NetworthReport, filter store.NetworthFilter) string {
	if report.Accounts == 0 {
		return "no accounts yet; run moneta link to connect an institution, then moneta sync"
	}
	if report.MissingBalance == report.Accounts && filter.AsOf != "" {
		return fmt.Sprintf(
			"no balance snapshots on or before %s; choose a later --as-of date or run moneta sync",
			filter.AsOf,
		)
	}
	if report.MissingBalance > 0 {
		return "run moneta sync to pull balances for accounts without an eligible snapshot"
	}
	return "run moneta accounts to inspect account-level balances"
}

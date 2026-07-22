package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// runDebts prints credit-card and loan balances with best-effort terms. It is
// read-only against the local database. Exit codes: 0 ok, 1 runtime error, 2
// usage.
func runDebts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("debts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: debts does not accept positional arguments")
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

	report, err := store.ReadDebts(ctx, database)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildDebtsDoc(report), format); err != nil {
		fmt.Fprintf(stderr, "error: render debts: %v\n", err)
		return 1
	}
	return 0
}

func buildDebtsDoc(report store.DebtReport) toon.Object {
	table := toon.Table{
		Fields: []string{"name", "type", "balance", "limit", "utilization", "apr", "due_day"},
		Rows:   make([][]any, 0, len(report.Debts)),
	}
	for _, debt := range report.Debts {
		balance := any(nil)
		if debt.BalanceCents != nil {
			balance = cli.Money(*debt.BalanceCents)
		}
		limit := any(nil)
		if debt.LimitCents != nil {
			limit = cli.Money(*debt.LimitCents)
		}
		utilization := any(nil)
		if debt.BalanceCents != nil && debt.LimitCents != nil {
			if value := cli.Ratio(*debt.BalanceCents, *debt.LimitCents, 4); value != nil {
				utilization = *value
			}
		}
		apr := any(nil)
		if debt.APRBasisPoints != nil {
			apr = cli.ScaledInteger(*debt.APRBasisPoints, 4)
		}
		dueDay := any(nil)
		if debt.DueDay != nil {
			dueDay = *debt.DueDay
		}
		table.Rows = append(table.Rows, []any{
			debt.Name, debt.Type, balance, limit, utilization, apr, dueDay,
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: report.Count},
			{Key: "total_debt", Value: cli.Money(report.TotalDebtCents)},
			{Key: "missing_balance", Value: report.MissingBalance},
		}},
		{Key: "debts", Value: table},
		{Key: "hint", Value: debtsHint(report)},
	}
}

func debtsHint(report store.DebtReport) string {
	if report.Count == 0 {
		return "no credit-card or loan accounts yet; run moneta sync"
	}
	if report.MissingBalance > 0 {
		return "run moneta sync to pull balances for debt accounts with no snapshot"
	}
	return "run moneta networth to compare total debt with assets"
}

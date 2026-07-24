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

// runCards prints credit-card balances, limits, utilization, APR, and due days.
// Loans stay in moneta debts. It is read-only against the local database. Exit
// codes: 0 ok, 1 runtime error, 2 usage.
func runCards(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cards", flag.ContinueOnError)
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
		fmt.Fprintln(stderr, "error: cards does not accept positional arguments")
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

	report, err := store.ReadCards(ctx, database)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildCardsDoc(report), format); err != nil {
		fmt.Fprintf(stderr, "error: render cards: %v\n", err)
		return 1
	}
	return 0
}

// buildCardsDoc mirrors the debts document without the type column, because
// every row is a credit card. Utilization stays null unless both a balance and
// a positive limit exist, so a missing limit never reads as 0%.
func buildCardsDoc(report store.DebtReport) toon.Object {
	table := toon.Table{
		Fields: []string{"name", "balance", "limit", "utilization", "apr", "due_day"},
		Rows:   make([][]any, 0, len(report.Debts)),
	}
	for _, card := range report.Debts {
		balance := any(nil)
		if card.BalanceCents != nil {
			balance = cli.Money(*card.BalanceCents)
		}
		limit := any(nil)
		if card.LimitCents != nil {
			limit = cli.Money(*card.LimitCents)
		}
		utilization := any(nil)
		if card.BalanceCents != nil && card.LimitCents != nil {
			if value := cli.Ratio(*card.BalanceCents, *card.LimitCents, 4); value != nil {
				utilization = *value
			}
		}
		apr := any(nil)
		if card.APRBasisPoints != nil {
			apr = cli.ScaledInteger(*card.APRBasisPoints, 4)
		}
		dueDay := any(nil)
		if card.DueDay != nil {
			dueDay = *card.DueDay
		}
		table.Rows = append(table.Rows, []any{
			card.Name, balance, limit, utilization, apr, dueDay,
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: report.Count},
			{Key: "total_debt", Value: cli.Money(report.TotalDebtCents)},
			{Key: "missing_balance", Value: report.MissingBalance},
		}},
		{Key: "cards", Value: table},
		{Key: "hint", Value: cardsHint(report)},
	}
}

func cardsHint(report store.DebtReport) string {
	if report.Count == 0 {
		return "no credit-card accounts yet; run moneta sync (loans stay in moneta debts)"
	}
	if report.MissingBalance > 0 {
		return "run moneta sync to pull balances for cards with no snapshot"
	}
	return "run moneta debts to include loan balances"
}

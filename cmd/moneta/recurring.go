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
	"github.com/jmoneytech-stack/moneta/internal/report"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

// runRecurring lists recurring series and manual rows from the local database.
// Exit codes are 0 success, 1 runtime error, and 2 usage.
func runRecurring(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runRecurringAt(ctx, args, stdout, stderr, time.Now())
}

func runRecurringAt(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("recurring", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	kind := flags.String(
		"kind",
		"",
		"filter listed rows by subscription, bill, or income",
	)
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: recurring does not accept positional arguments")
		return 2
	}
	switch *kind {
	case "", "subscription", "bill", "income":
	default:
		fmt.Fprintln(stderr, "error: --kind must be subscription, bill, or income")
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
	recurringReport, err := store.ReadRecurring(ctx, database, store.RecurringFilter{
		AsOf: now.Format(time.DateOnly),
		Kind: *kind,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, report.Recurring(recurringReport), format); err != nil {
		fmt.Fprintf(stderr, "error: render recurring: %v\n", err)
		return 1
	}
	return 0
}

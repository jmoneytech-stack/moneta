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

// runBills lists recurring obligations and active credit-card dues. Exit
// codes are 0 success, 1 runtime error, and 2 usage.
func runBills(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runBillsAt(ctx, args, stdout, stderr, time.Now())
}

func runBillsAt(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("bills", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	days := flags.Int("days", 30, "calendar days after today to include (1-366)")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: bills does not accept positional arguments")
		return 2
	}
	if *days < 1 || *days > 366 {
		fmt.Fprintln(stderr, "error: --days must be from 1 to 366")
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
	billsReport, err := store.ReadBills(
		ctx,
		database,
		now.Format(time.DateOnly),
		*days,
	)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, report.Bills(billsReport), format); err != nil {
		fmt.Fprintf(stderr, "error: render bills: %v\n", err)
		return 1
	}
	return 0
}

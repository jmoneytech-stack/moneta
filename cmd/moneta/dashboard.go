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

// runDashboard prints the content-first summary an agent reads first: net
// worth, cash, credit utilization, this month's spend and cashflow, and sync
// health. It composes existing reads and computes nothing new.
//
// The dashboard is an explicit subcommand; bare 'moneta' still prints usage
// and exits 2 (R3(b)/B1).
func runDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runDashboardAt(ctx, args, stdout, stderr, time.Now())
}

// runDashboardAt is runDashboard with an injectable clock; the current local
// month selects the spend and cashflow window. Exit codes match moneta status
// so scripts can detect reconnection the same way: 0 ok, 1 runtime error, 2
// usage, 3 an Item needs reconnection.
func runDashboardAt(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
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
		fmt.Fprintln(stderr, "error: dashboard does not accept positional arguments")
		return 2
	}
	if *databasePath == "" {
		fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}

	period, err := resolveReadPeriod("", "", "", now)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	dashboard, err := store.ReadDashboard(ctx, database, store.DashboardFilter{
		From: period.From,
		To:   period.To,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, report.Dashboard(dashboard), format); err != nil {
		fmt.Fprintf(stderr, "error: render dashboard: %v\n", err)
		return 1
	}
	if dashboard.Sync.LoginRequired > 0 {
		return 3
	}
	return 0
}

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

// runAnomalies lists category-level spend spikes. Exit codes are 0 success, 1
// runtime error, and 2 usage.
func runAnomalies(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runAnomaliesAt(ctx, args, stdout, stderr, time.Now())
}

func runAnomaliesAt(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	now time.Time,
) int {
	flags := flag.NewFlagSet("anomalies", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	period := flags.String(
		"period",
		"",
		"calendar month in YYYY-MM (default previous complete month)",
	)
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: anomalies does not accept positional arguments")
		return 2
	}
	asOf := now.Format(time.DateOnly)
	if err := store.ValidateAnomalyPeriod(asOf, *period); err != nil {
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
	anomalyReport, err := store.ReadAnomalies(ctx, database, asOf, *period)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, report.Anomalies(anomalyReport), format); err != nil {
		fmt.Fprintf(stderr, "error: render anomalies: %v\n", err)
		return 1
	}
	return 0
}

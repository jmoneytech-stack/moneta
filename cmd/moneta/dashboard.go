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

// phase4Note explains the two deliberately empty dashboard slots. Upcoming
// bills need recurring detection and anomalies need a baseline engine; both
// are Phase 4. They render as null rather than 0 so an agent can tell "not
// implemented" from "none found".
const phase4Note = "upcoming_bills and anomalies are available in a later phase"

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

	report, err := store.ReadDashboard(ctx, database, store.DashboardFilter{
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
	if err := cli.Render(stdout, buildDashboardDoc(report), format); err != nil {
		fmt.Fprintf(stderr, "error: render dashboard: %v\n", err)
		return 1
	}
	if report.Sync.LoginRequired > 0 {
		return 3
	}
	return 0
}

func buildDashboardDoc(report store.DashboardReport) toon.Object {
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "as_of", Value: dashboardAsOf(report)},
		}},
		{Key: "networth", Value: toon.Object{
			{Key: "assets", Value: cli.Money(report.Networth.AssetsCents)},
			{Key: "liabilities", Value: cli.Money(report.Networth.LiabilitiesCents)},
			{Key: "networth", Value: cli.Money(report.Networth.NetworthCents)},
			{Key: "accounts", Value: report.Networth.Accounts},
			{Key: "missing_balance", Value: report.Networth.MissingBalance},
		}},
		{Key: "cash", Value: toon.Object{
			{Key: "balance", Value: cli.Money(report.CashCents)},
			{Key: "accounts", Value: report.CashAccounts},
			{Key: "note", Value: "checking + savings latest balances"},
		}},
		{Key: "credit", Value: toon.Object{
			{Key: "utilization", Value: dashboardUtilization(report)},
			{Key: "total_debt", Value: cli.Money(report.CardDebtCents)},
			{Key: "cards", Value: report.CardCount},
		}},
		{Key: "spend_month", Value: toon.Object{
			{Key: "from", Value: report.From},
			{Key: "to", Value: report.To},
			{Key: "total", Value: cli.Money(report.Spend.SpendCents)},
			{Key: "count", Value: report.Spend.Count},
		}},
		{Key: "cashflow_month", Value: toon.Object{
			{Key: "inflow", Value: cli.Money(report.Cashflow.InflowCents)},
			{Key: "outflow", Value: cli.Money(report.Cashflow.OutflowCents)},
			{Key: "net", Value: cli.Money(report.Cashflow.NetCents)},
			{Key: "savings_rate", Value: dashboardSavingsRate(report)},
			{Key: "count", Value: report.Cashflow.Count},
		}},
		{Key: "sync", Value: toon.Object{
			{Key: "items", Value: report.Sync.Items},
			{Key: "needs_attention", Value: report.Sync.NeedsAttention},
			{Key: "login_required", Value: report.Sync.LoginRequired},
		}},
		{Key: "upcoming_bills", Value: nil},
		{Key: "anomalies", Value: nil},
		{Key: "phase4_note", Value: phase4Note},
		{Key: "hint", Value: dashboardHint(report)},
	}
}

// dashboardAsOf is the latest balance date across accounts. It is null rather
// than a fabricated today when no account has a snapshot yet.
func dashboardAsOf(report store.DashboardReport) any {
	if report.AsOf == "" {
		return nil
	}
	return report.AsOf
}

// dashboardUtilization is the credit-card portfolio ratio for "now": summed
// balances over summed limits across cards that have both a balance snapshot
// and a positive limit. It is null when no card qualifies, so a missing limit
// never reads as 0%.
func dashboardUtilization(report store.DashboardReport) any {
	if report.UtilizationCards == 0 {
		return nil
	}
	value := cli.Ratio(report.UtilizationDebtCents, report.UtilizationLimitCents, 4)
	if value == nil {
		return nil
	}
	return *value
}

func dashboardSavingsRate(report store.DashboardReport) any {
	value := cli.Ratio(report.Cashflow.NetCents, report.Cashflow.InflowCents, 4)
	if value == nil {
		return nil
	}
	return *value
}

// dashboardHint is the single next step an agent should take.
func dashboardHint(report store.DashboardReport) string {
	if report.Sync.LoginRequired > 0 {
		return "re-run moneta link to reconnect items with status login_required"
	}
	if report.Sync.Items == 0 {
		return "run moneta link to connect an institution, then moneta sync"
	}
	if report.Networth.MissingBalance > 0 {
		return "run moneta sync to pull balances for accounts with no snapshot"
	}
	return "run moneta spend or moneta trends for the breakdown behind these totals"
}

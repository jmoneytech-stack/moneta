package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

const savingsRateDecimalPlaces = 4

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

// savingsRateNumber returns net/inflow as a decimal fraction truncated toward
// zero to four decimal places (one basis point). For example 0.1234 means
// 12.34%. Big integers avoid overflow and all float precision concerns. A
// zero inflow returns nil so the output boundary emits null.
func savingsRateNumber(netCents, inflowCents int64) *toon.Number {
	if inflowCents <= 0 {
		return nil
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(savingsRateDecimalPlaces), nil)
	numerator := new(big.Int).Mul(big.NewInt(netCents), scale)
	scaled := new(big.Int).Quo(numerator, big.NewInt(inflowCents))
	value := toon.Number(formatScaledInteger(scaled, savingsRateDecimalPlaces))
	return &value
}

func formatScaledInteger(value *big.Int, decimalPlaces int) string {
	negative := value.Sign() < 0
	magnitude := new(big.Int).Abs(new(big.Int).Set(value)).String()
	if len(magnitude) <= decimalPlaces {
		magnitude = strings.Repeat("0", decimalPlaces-len(magnitude)+1) + magnitude
	}
	split := len(magnitude) - decimalPlaces
	whole := magnitude[:split]
	fraction := strings.TrimRight(magnitude[split:], "0")
	formatted := whole
	if fraction != "" {
		formatted += "." + fraction
	}
	if negative && formatted != "0" {
		formatted = "-" + formatted
	}
	return formatted
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

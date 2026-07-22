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

// runTx prints transactions with an aggregate header as TOON (or JSON with
// --json). Read-only against the local database: no provider calls, no
// decrypted credentials. Exit codes: 0 ok, 1 error, 2 usage.
//
// The v1 filter set is deliberately small: --from/--to (inclusive dates),
// --account (case-insensitive substring of the account name), and --search
// (case-insensitive merchant substring). Deferred plan filters: --cat,
// --merchant, --entity, --min/--max.
func runTx(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tx", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	from := flags.String("from", "", "earliest transaction date, YYYY-MM-DD (inclusive)")
	to := flags.String("to", "", "latest transaction date, YYYY-MM-DD (inclusive)")
	account := flags.String(
		"account",
		"",
		"filter to accounts whose name contains this substring (case-insensitive)",
	)
	search := flags.String(
		"search",
		"",
		"filter to transactions whose merchant contains this substring (case-insensitive)",
	)
	limit := flags.Int("limit", statusDefaultLimit, "maximum transaction rows to show")
	full := flags.Bool("full", false, "show all matching rows, ignoring --limit")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: tx does not accept positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
		return 2
	}
	if err := validateCLIDate("from", *from); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if err := validateCLIDate("to", *to); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if *from != "" && *to != "" && *from > *to {
		fmt.Fprintln(stderr, "error: --from must not be after --to")
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

	filter := store.TransactionFilter{
		From:    *from,
		To:      *to,
		Account: *account,
		Search:  *search,
	}
	summary, err := store.SummarizeTransactions(ctx, database, filter)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	rowLimit := 0
	if !*full {
		rowLimit = *limit
	}
	transactions, err := store.ListTransactions(ctx, database, filter, rowLimit)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	doc := buildTxDoc(summary, transactions, filter)
	if err := cli.Render(stdout, doc, format); err != nil {
		fmt.Fprintf(stderr, "error: render tx: %v\n", err)
		return 1
	}
	return 0
}

func validateCLIDate(flagName string, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("--%s must be a valid YYYY-MM-DD date, got %q", flagName, value)
	}
	return nil
}

// buildTxDoc shapes the transactions document: an aggregate summary over
// every match (not just the shown window), with money totals restricted to
// non-excluded rows per the analytics-exclusion rule, one tx table, an
// optional truncation line, and a next-step hint. Amounts are signed
// dollars at the output boundary; negative = outflow.
func buildTxDoc(
	summary store.TransactionSummary,
	transactions []store.TransactionRow,
	filter store.TransactionFilter,
) toon.Object {
	table := toon.Table{
		Fields: []string{"date", "amount", "merchant", "status", "account"},
		Rows:   make([][]any, 0, len(transactions)),
	}
	for _, transaction := range transactions {
		table.Rows = append(table.Rows, []any{
			transaction.Date,
			cli.Money(transaction.AmountCents),
			transaction.Merchant,
			transaction.Status,
			transaction.AccountName,
		})
	}

	doc := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: summary.Count},
			{Key: "excluded_count", Value: summary.ExcludedCount},
			{Key: "total", Value: cli.Money(summary.TotalCents)},
			{Key: "inflow", Value: cli.Money(summary.InflowCents)},
			{Key: "outflow", Value: cli.Money(summary.OutflowCents)},
		}},
		{Key: "tx", Value: table},
	}
	if len(transactions) < summary.Count {
		doc = append(doc, toon.Field{
			Key: "truncated",
			Value: fmt.Sprintf(
				"%d of %d shown (--full for all; pipe | grep to filter)",
				len(transactions),
				summary.Count,
			),
		})
	}
	doc = append(doc, toon.Field{Key: "hint", Value: txHint(summary.Count, filter)})
	return doc
}

// txHint gives the definitive next step: a widening suggestion when the
// filter matched nothing, otherwise the natural follow-up reads.
func txHint(count int, filter store.TransactionFilter) string {
	if count > 0 {
		return "run moneta accounts for balances; moneta status for sync health"
	}
	if filter.From != "" || filter.To != "" || filter.Account != "" || filter.Search != "" {
		return "no matches; widen the date range or relax --account/--search filters"
	}
	return "no transactions yet; run moneta link to connect an institution, then moneta sync"
}

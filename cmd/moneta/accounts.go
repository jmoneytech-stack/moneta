package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// runAccounts prints accounts with their latest balance snapshot as TOON (or
// JSON with --json). Read-only against the local database: no provider
// calls, no decrypted credentials. Exit codes: 0 ok, 1 error, 2 usage.
func runAccounts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("accounts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	accountType := flags.String(
		"type",
		"",
		"filter to one account type (checking|savings|credit_card|loan|investment|asset)",
	)
	limit := flags.Int("limit", statusDefaultLimit, "maximum account rows to show")
	full := flags.Bool("full", false, "show all account rows, ignoring --limit")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: accounts does not accept positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
		return 2
	}
	if *accountType != "" && !validCLAccountType(*accountType) {
		fmt.Fprintf(stderr, "error: unknown account type %q\n", *accountType)
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

	accounts, err := store.ListAccountSummaries(ctx, database, *accountType)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildAccountsDoc(accounts, *accountType, *limit, *full), format); err != nil {
		fmt.Fprintf(stderr, "error: render accounts: %v\n", err)
		return 1
	}
	return 0
}

func validCLAccountType(accountType string) bool {
	switch canon.AccountType(accountType) {
	case canon.AccountTypeChecking,
		canon.AccountTypeSavings,
		canon.AccountTypeCreditCard,
		canon.AccountTypeLoan,
		canon.AccountTypeInvestment,
		canon.AccountTypeAsset:
		return true
	default:
		return false
	}
}

// buildAccountsDoc shapes the accounts document: a summary block with
// counts (total, active, per type), one accounts table with the plan's
// four-field default schema (name, type, balance, status), an optional
// truncation line, and a next-step hint. Balances are dollars at the output
// boundary, null when an account has no snapshot yet.
func buildAccountsDoc(accounts []store.AccountSummary, typeFilter string, limit int, full bool) toon.Object {
	active := 0
	byType := toon.Object{}
	typeCounts := make(map[string]int)
	typeOrder := []string{}
	for _, account := range accounts {
		if account.Active {
			active++
		}
		if _, seen := typeCounts[account.Type]; !seen {
			typeOrder = append(typeOrder, account.Type)
		}
		typeCounts[account.Type]++
	}
	for _, accountType := range typeOrder {
		byType = append(byType, toon.Field{Key: accountType, Value: typeCounts[accountType]})
	}

	shown := accounts
	if !full && len(accounts) > limit {
		shown = accounts[:limit]
	}
	table := toon.Table{
		Fields: []string{"name", "type", "balance", "status"},
		Rows:   make([][]any, 0, len(shown)),
	}
	for _, account := range shown {
		balance := any(nil)
		if account.BalanceCents != nil {
			balance = cli.Money(*account.BalanceCents)
		}
		status := "active"
		if !account.Active {
			status = "inactive"
		}
		table.Rows = append(table.Rows, []any{
			account.Name,
			account.Type,
			balance,
			status,
		})
	}

	doc := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "accounts", Value: len(accounts)},
			{Key: "active", Value: active},
			{Key: "by_type", Value: byType},
		}},
		{Key: "accounts", Value: table},
	}
	if len(shown) < len(accounts) {
		doc = append(doc, toon.Field{
			Key:   "truncated",
			Value: fmt.Sprintf("%d of %d shown (--full for all)", len(shown), len(accounts)),
		})
	}
	doc = append(doc, toon.Field{Key: "hint", Value: accountsHint(accounts, typeFilter)})
	return doc
}

// accountsHint distinguishes a truly empty database from a filter that
// matched nothing, mirroring txHint: only the truly empty state points at
// moneta link.
func accountsHint(accounts []store.AccountSummary, typeFilter string) string {
	if len(accounts) == 0 {
		if typeFilter != "" {
			return fmt.Sprintf("no accounts match --type %s; relax the filter or run moneta sync", typeFilter)
		}
		return "no accounts yet; run moneta link to connect an institution, then moneta sync"
	}
	for _, account := range accounts {
		if account.BalanceCents == nil {
			return "run moneta sync to pull balances for accounts without a snapshot"
		}
	}
	return "run moneta tx to inspect transactions; moneta status for sync health"
}

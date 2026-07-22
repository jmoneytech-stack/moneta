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

// statusDefaultLimit bounds the items table per the AXI truncation
// convention; --full or a larger --limit shows every row.
const statusDefaultLimit = 20

// runStatus prints linked provider items, their stored health, and last-sync
// signals as TOON (or JSON with --json). It reads only the local database:
// no provider calls, no decrypted credentials, no amounts. It returns the
// process exit code directly because it uses the full AXI exit-code set:
// 0 ok, 1 error, 2 usage, 3 reconnection needed.
func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	limit := flags.Int(
		"limit",
		statusDefaultLimit,
		"maximum item rows to show",
	)
	full := flags.Bool("full", false, "show all item rows, ignoring --limit")
	asJSON := flags.Bool("json", false, "emit JSON instead of TOON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: status does not accept positional arguments")
		return 2
	}
	if *limit < 1 {
		fmt.Fprintln(stderr, "error: --limit must be at least 1")
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

	items, err := store.ListProviderItemStatuses(ctx, database)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	format := cli.FormatTOON
	if *asJSON {
		format = cli.FormatJSON
	}
	if err := cli.Render(stdout, buildStatusDoc(items, *limit, *full), format); err != nil {
		fmt.Fprintf(stderr, "error: render status: %v\n", err)
		return 1
	}

	for _, item := range items {
		if item.Status == "login_required" {
			return 3
		}
	}
	return 0
}

// buildStatusDoc shapes the status document: a summary block with
// pre-computed counts, one items table, an optional truncation line, and a
// next-step hint. Counts and timestamps only; no amounts or account names.
func buildStatusDoc(items []store.ProviderItemStatus, limit int, full bool) toon.Object {
	accounts := 0
	attention := 0
	for _, item := range items {
		accounts += item.Accounts
		if item.Status != "ok" {
			attention++
		}
	}

	shown := items
	if !full && len(items) > limit {
		shown = prioritizeAttention(items, limit)
	}
	table := toon.Table{
		Fields: []string{
			"provider", "item", "institution", "status",
			"accounts", "transactions", "last_sync",
		},
		Rows: make([][]any, 0, len(shown)),
	}
	for _, item := range shown {
		table.Rows = append(table.Rows, []any{
			item.Provider,
			item.ItemID,
			item.Institution,
			item.Status,
			item.Accounts,
			item.Transactions,
			item.LastSyncedAt,
		})
	}

	doc := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "items", Value: len(items)},
			{Key: "accounts", Value: accounts},
			{Key: "needs_attention", Value: attention},
		}},
		{Key: "items", Value: table},
	}
	if len(shown) < len(items) {
		doc = append(doc, toon.Field{
			Key:   "truncated",
			Value: fmt.Sprintf("%d of %d shown (--full for all)", len(shown), len(items)),
		})
	}
	doc = append(doc, toon.Field{Key: "hint", Value: statusHint(items)})
	return doc
}

// prioritizeAttention selects the shown window when truncating. Rows
// needing attention (status != ok) enter the window first so an agent never
// has to pass --full to see a reconnection signal; the store's provider and
// item id ordering is preserved within each group.
func prioritizeAttention(items []store.ProviderItemStatus, limit int) []store.ProviderItemStatus {
	shown := make([]store.ProviderItemStatus, 0, limit)
	for _, item := range items {
		if item.Status != "ok" && len(shown) < limit {
			shown = append(shown, item)
		}
	}
	for _, item := range items {
		if item.Status == "ok" && len(shown) < limit {
			shown = append(shown, item)
		}
	}
	return shown
}

// statusHint is the definitive next step for an agent reading the output.
func statusHint(items []store.ProviderItemStatus) string {
	if len(items) == 0 {
		return "run moneta link to connect an institution, then moneta sync"
	}
	needsReconnect := false
	neverSynced := false
	for _, item := range items {
		if item.Status == "login_required" {
			needsReconnect = true
		}
		if item.LastSyncedAt == "" {
			neverSynced = true
		}
	}
	if needsReconnect {
		return "re-run moneta link to reconnect items with status login_required"
	}
	if neverSynced {
		return "run moneta sync to pull data for never-synced items"
	}
	return "run moneta sync to refresh balances and transactions"
}

package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/core"
	"github.com/jmoneytech-stack/moneta/internal/providers/plaid"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const (
	databasePathEnvironment = "MONETA_DB_PATH"
	plaidProviderName       = "plaid"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "link":
		return runLink(ctx, args[1:], stdout, stderr)
	case "sync":
		return runSync(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "accounts":
		return runAccounts(ctx, args[1:], stdout, stderr)
	case "tx":
		return runTx(ctx, args[1:], stdout, stderr)
	case "spend":
		return runSpend(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// runLink shares the exit-code contract with the read commands: 0 ok, 1
// runtime error, 2 usage. The flag package already prints parse errors, so
// they return 2 without a second message. Ctrl+C (context.Canceled) exits 1
// silently.
func runLink(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("link", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	listenAddress := flags.String(
		"listen",
		"127.0.0.1:0",
		"loopback address for the temporary Plaid Link server",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: link does not accept positional arguments")
		return 2
	}
	if *databasePath == "" {
		fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}

	config, err := plaid.ConfigFromEnvironment()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	cipher, err := secret.FromEnvironment()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer database.Close()

	linker, err := plaid.NewLinker(config, database, cipher)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	logger := log.New(stderr, "", log.LstdFlags)
	server, err := plaid.NewLinkServer(linker, plaid.LinkServerConfig{
		ListenAddress: *listenAddress,
		Logger:        logger,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	session, err := server.Start(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = session.Close(closeCtx)
	}()

	fmt.Fprintf(stdout, "Open %s in your browser to connect an institution.\n", session.URL)
	item, err := session.Wait(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}
	fmt.Fprintf(stdout, "Linked %s (%s).\n", item.Institution, item.ItemID)
	return 0
}

// runSync shares the exit-code contract with the read commands: 0 ok, 1
// runtime error, 2 usage.
func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	itemID := flags.String(
		"item",
		"",
		"sync only the Plaid item with this id (default: all linked items)",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: sync does not accept positional arguments")
		return 2
	}
	if *databasePath == "" {
		fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}

	config, err := plaid.ConfigFromEnvironment()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	cipher, err := secret.FromEnvironment()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer database.Close()

	var items []store.ProviderItem
	if *itemID != "" {
		item, err := store.GetProviderItem(ctx, database, plaidProviderName, *itemID)
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(stderr, "error: provider item %q is not linked\n", *itemID)
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		items = []store.ProviderItem{item}
	} else {
		items, err = store.ListProviderItems(ctx, database, plaidProviderName)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	if err := syncItems(ctx, database, cipher, items, func(
		item store.ProviderItem,
		accessToken string,
	) (canon.Provider, error) {
		return plaid.New(config, item.ItemID, item.Institution, accessToken)
	}, stdout, stderr); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}
	return 0
}

// syncItems runs the library sync path for each item and prints a per-item
// summary. Output carries institution names and skip counts only: never
// amounts, account names, or credentials.
func syncItems(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	items []store.ProviderItem,
	buildProvider func(item store.ProviderItem, accessToken string) (canon.Provider, error),
	stdout, stderr io.Writer,
) error {
	if len(items) == 0 {
		fmt.Fprintln(stdout, "No linked provider items. Run 'moneta link' to connect an institution.")
		return nil
	}

	synced := 0
	skipped := 0
	for _, item := range items {
		result, err := core.SyncProviderItem(ctx, db, cipher, item, func(
			accessToken string,
		) (canon.Provider, error) {
			return buildProvider(item, accessToken)
		})
		if err != nil {
			// A reauth-class failure durably marks the Item so 'moneta
			// status' can exit 3. This write runs after the failed sync
			// returned, outside the rolled-back batch transaction. The
			// message carries institution and item id only - never tokens
			// or raw provider payloads.
			if plaid.IsLoginRequired(err) {
				if statusErr := store.SetProviderItemStatus(
					ctx, db, plaidProviderName, item.ItemID, "login_required",
				); statusErr != nil {
					fmt.Fprintf(
						stderr,
						"error: persist login_required for item %s: %v\n",
						item.ItemID,
						statusErr,
					)
				} else {
					fmt.Fprintf(
						stderr,
						"item %s (%s) needs reconnection; re-run moneta link\n",
						item.ItemID,
						item.Institution,
					)
				}
			}
			if errors.Is(err, core.ErrCursorChanged) {
				fmt.Fprintf(
					stderr,
					"error: sync item %s: cursor changed during sync; retry\n",
					item.ItemID,
				)
			} else {
				fmt.Fprintf(stderr, "error: sync item %s: %v\n", item.ItemID, err)
			}
			continue
		}
		synced++
		skipped += len(result.Skipped)
		if len(result.Skipped) > 0 {
			fmt.Fprintf(
				stdout,
				"Synced %s: %s skipped.\n",
				item.Institution,
				recordPhrase(len(result.Skipped)),
			)
		} else {
			fmt.Fprintf(stdout, "Synced %s.\n", item.Institution)
		}
	}

	fmt.Fprintf(stdout, "Synced %d of %d items", synced, len(items))
	if skipped > 0 {
		fmt.Fprintf(stdout, ", %s skipped", recordPhrase(skipped))
	}
	fmt.Fprintln(stdout, ".")
	if synced != len(items) {
		return fmt.Errorf("%d of %d items failed to sync", len(items)-synced, len(items))
	}
	return nil
}

func recordPhrase(count int) string {
	if count == 1 {
		return "1 record"
	}
	return fmt.Sprintf("%d records", count)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: moneta <command>")
	fmt.Fprintln(writer, "commands:")
	fmt.Fprintln(writer, "  link    connect an institution through Plaid Link")
	fmt.Fprintln(writer, "  sync    sync transactions, balances, and liabilities for linked items")
	fmt.Fprintln(writer, "  status  show linked items, health, and last-sync signals (TOON on stdout)")
	fmt.Fprintln(writer, "  accounts  list accounts with latest balances (TOON on stdout)")
	fmt.Fprintln(writer, "  tx        list transactions with an aggregate header (TOON on stdout)")
	fmt.Fprintln(writer, "  spend     summarize posted spending by category and merchant (TOON on stdout)")
}

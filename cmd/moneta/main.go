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
		if err := runLink(ctx, args[1:], stdout, stderr); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			if !errors.Is(err, context.Canceled) {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
			return 1
		}
		return 0
	case "sync":
		if err := runSync(ctx, args[1:], stdout, stderr); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			if !errors.Is(err, context.Canceled) {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
			return 1
		}
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runLink(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("link does not accept positional arguments")
	}
	if *databasePath == "" {
		return fmt.Errorf("MONETA_DB_PATH or --db is required")
	}

	config, err := plaid.ConfigFromEnvironment()
	if err != nil {
		return err
	}
	cipher, err := secret.FromEnvironment()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer database.Close()

	linker, err := plaid.NewLinker(config, database, cipher)
	if err != nil {
		return err
	}
	logger := log.New(stderr, "", log.LstdFlags)
	server, err := plaid.NewLinkServer(linker, plaid.LinkServerConfig{
		ListenAddress: *listenAddress,
		Logger:        logger,
	})
	if err != nil {
		return err
	}
	session, err := server.Start(ctx)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = session.Close(closeCtx)
	}()

	fmt.Fprintf(stdout, "Open %s in your browser to connect an institution.\n", session.URL)
	item, err := session.Wait(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Linked %s (%s).\n", item.Institution, item.ItemID)
	return nil
}

func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("sync does not accept positional arguments")
	}
	if *databasePath == "" {
		return fmt.Errorf("MONETA_DB_PATH or --db is required")
	}

	config, err := plaid.ConfigFromEnvironment()
	if err != nil {
		return err
	}
	cipher, err := secret.FromEnvironment()
	if err != nil {
		return err
	}
	database, err := store.Open(ctx, *databasePath)
	if err != nil {
		return err
	}
	defer database.Close()

	var items []store.ProviderItem
	if *itemID != "" {
		item, err := store.GetProviderItem(ctx, database, plaidProviderName, *itemID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("provider item %q is not linked", *itemID)
		}
		if err != nil {
			return err
		}
		items = []store.ProviderItem{item}
	} else {
		items, err = store.ListProviderItems(ctx, database, plaidProviderName)
		if err != nil {
			return err
		}
	}

	return syncItems(ctx, database, cipher, items, func(
		item store.ProviderItem,
		accessToken string,
	) (canon.Provider, error) {
		return plaid.New(config, item.ItemID, item.Institution, accessToken)
	}, stdout, stderr)
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
}

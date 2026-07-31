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
	"github.com/jmoneytech-stack/moneta/internal/recurring"
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
	case "cashflow":
		return runCashflow(ctx, args[1:], stdout, stderr)
	case "networth":
		return runNetworth(ctx, args[1:], stdout, stderr)
	case "trends":
		return runTrends(ctx, args[1:], stdout, stderr)
	case "debts":
		return runDebts(ctx, args[1:], stdout, stderr)
	case "cards":
		return runCards(ctx, args[1:], stdout, stderr)
	case "recurring":
		return runRecurring(ctx, args[1:], stdout, stderr)
	case "bills":
		return runBills(ctx, args[1:], stdout, stderr)
	case "dashboard":
		return runDashboard(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stdout, stderr)
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
	defer func() { _ = database.Close() }()

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
	resetCursor := flags.Bool(
		"reset-cursor",
		false,
		"re-pull full available transaction history without clearing the stored checkpoint",
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
	defer func() { _ = database.Close() }()

	allItems, err := store.ListProviderItems(ctx, database, plaidProviderName)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	items := allItems
	if *itemID != "" {
		items = nil
		for _, item := range allItems {
			if item.ItemID == *itemID {
				items = []store.ProviderItem{item}
				break
			}
		}
		if len(items) == 0 {
			fmt.Fprintf(stderr, "error: provider item %q is not linked\n", *itemID)
			return 1
		}
	}

	if err := syncItemsWithDetection(ctx, database, cipher, allItems, items, func(
		item store.ProviderItem,
		accessToken string,
	) (canon.Provider, error) {
		return plaid.New(config, item.ItemID, item.Institution, accessToken)
	}, *resetCursor, syncDetectionDependencies{}, stdout, stderr); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 1
	}
	return 0
}

// syncDetectionDependencies is the post-loop test seam. Production leaves it
// empty to use the wall clock and pure recurring detector.
type syncDetectionDependencies struct {
	now    func() time.Time
	detect func([]recurring.Candidate, string) (recurring.Result, error)
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
	return syncItemsWithDetection(
		ctx, db, cipher, items, items, buildProvider, false,
		syncDetectionDependencies{}, stdout, stderr,
	)
}

func syncItemsWithReset(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	items []store.ProviderItem,
	buildProvider func(item store.ProviderItem, accessToken string) (canon.Provider, error),
	resetCursor bool,
	stdout, stderr io.Writer,
) error {
	return syncItemsWithDetection(
		ctx, db, cipher, items, items, buildProvider, resetCursor,
		syncDetectionDependencies{}, stdout, stderr,
	)
}

func syncItemsWithDetection(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	allItems []store.ProviderItem,
	items []store.ProviderItem,
	buildProvider func(item store.ProviderItem, accessToken string) (canon.Provider, error),
	resetCursor bool,
	dependencies syncDetectionDependencies,
	stdout, stderr io.Writer,
) error {
	if len(items) == 0 {
		fmt.Fprintln(stdout, "No linked provider items. Run 'moneta link' to connect an institution.")
		if db != nil {
			runPostLoopDetection(
				ctx, db, allItems, items, nil, dependencies, stdout, stderr,
			)
		}
		return nil
	}

	synced := 0
	skipped := 0
	successfulProviderItemIDs := make(map[int64]struct{}, len(items))
	for _, item := range items {
		pullCursor := item.SyncCursor
		if resetCursor {
			pullCursor = ""
		}
		result, err := core.SyncProviderItem(
			ctx,
			db,
			cipher,
			item,
			pullCursor,
			item.SyncCursor,
			func(
				accessToken string,
			) (canon.Provider, error) {
				return buildProvider(item, accessToken)
			},
		)
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
		successfulProviderItemIDs[item.DatabaseID] = struct{}{}
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
	runPostLoopDetection(
		ctx,
		db,
		allItems,
		items,
		successfulProviderItemIDs,
		dependencies,
		stdout,
		stderr,
	)
	if synced != len(items) {
		return fmt.Errorf("%d of %d items failed to sync", len(items)-synced, len(items))
	}
	return nil
}

func runPostLoopDetection(
	ctx context.Context,
	db *sql.DB,
	allItems []store.ProviderItem,
	items []store.ProviderItem,
	successfulProviderItemIDs map[int64]struct{},
	dependencies syncDetectionDependencies,
	stdout, stderr io.Writer,
) {
	now := time.Now
	if dependencies.now != nil {
		now = dependencies.now
	}
	detect := recurring.Detect
	if dependencies.detect != nil {
		detect = dependencies.detect
	}
	currentTime := now()
	runAt := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")
	if len(successfulProviderItemIDs) == 0 {
		if err := store.RecordDetectorAttempt(ctx, db, "partial", runAt, ""); err != nil {
			fmt.Fprintf(stderr, "error: record skipped recurring detection: %v\n", err)
		}
		fmt.Fprintln(stdout, "recurring: skipped (no item synced; see moneta status)")
		return
	}

	candidates, err := store.LoadRecurringCandidates(ctx, db, currentTime.Format(time.DateOnly))
	if err != nil {
		recordDetectionFailure(ctx, db, runAt, stdout, stderr)
		return
	}
	result, err := detect(candidates, currentTime.Format(time.DateOnly))
	if err != nil {
		recordDetectionFailure(ctx, db, runAt, stdout, stderr)
		return
	}
	completeRun := len(allItems) > 0 &&
		sameProviderItemSet(items, allItems) &&
		len(successfulProviderItemIDs) == len(items)
	if err := store.PersistRecurringDetection(ctx, db, store.RecurringDetectionInput{
		Result:                    result,
		Candidates:                candidates,
		SuccessfulProviderItemIDs: successfulProviderItemIDs,
		Complete:                  completeRun,
		RunAt:                     runAt,
		AsOf:                      currentTime.Format(time.DateOnly),
	}); err != nil {
		recordDetectionFailure(ctx, db, runAt, stdout, stderr)
		return
	}
	line := "recurring: partial (positive evidence only)"
	if completeRun {
		line = fmt.Sprintf("recurring: %d series", len(result.Series))
	}
	if result.SkippedOverflow > 0 {
		line += fmt.Sprintf(", %d overflow skipped", result.SkippedOverflow)
	}
	fmt.Fprintln(stdout, line)
}

func recordDetectionFailure(
	ctx context.Context,
	db *sql.DB,
	runAt string,
	stdout, stderr io.Writer,
) {
	if err := store.RecordDetectorAttempt(
		ctx, db, "error", runAt, "recurring detection failed",
	); err != nil {
		fmt.Fprintf(stderr, "error: record recurring detection failure: %v\n", err)
	}
	fmt.Fprintln(stdout, "recurring: detection failed")
}

func sameProviderItemSet(left, right []store.ProviderItem) bool {
	if len(left) != len(right) {
		return false
	}
	ids := make(map[int64]struct{}, len(left))
	for _, item := range left {
		ids[item.DatabaseID] = struct{}{}
	}
	if len(ids) != len(left) {
		return false
	}
	for _, item := range right {
		if _, found := ids[item.DatabaseID]; !found {
			return false
		}
	}
	return true
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
	fmt.Fprintln(writer, "  cashflow  summarize posted inflow, outflow, net, and savings rate (TOON on stdout)")
	fmt.Fprintln(writer, "  networth  summarize current or daily historical net worth (TOON on stdout)")
	fmt.Fprintln(writer, "  trends    compare compute-on-read financial trends (TOON on stdout)")
	fmt.Fprintln(writer, "  debts     list credit-card and loan balances with terms (TOON on stdout)")
	fmt.Fprintln(writer, "  cards     list credit cards with limit, utilization, APR, and due day (TOON on stdout)")
	fmt.Fprintln(writer, "  recurring list detected and manual recurring series (TOON on stdout)")
	fmt.Fprintln(writer, "  bills     list recurring obligations and active card dues (TOON on stdout)")
	fmt.Fprintln(writer, "  dashboard summarize net worth, cash, credit, this month, and sync health (TOON on stdout)")
	fmt.Fprintln(writer, "  serve     serve authenticated JSON reads over loopback HTTP")
}

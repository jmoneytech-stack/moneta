package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/providers/plaid"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const databasePathEnvironment = "MONETA_DB_PATH"

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

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: moneta <command>")
	fmt.Fprintln(writer, "commands:")
	fmt.Fprintln(writer, "  link    connect an institution through Plaid Link")
}

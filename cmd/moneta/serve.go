package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"github.com/jmoneytech-stack/moneta/internal/api"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const (
	apiKeyEnvironment    = "MONETA_API_KEY"
	defaultListenAddress = "127.0.0.1:8080"
)

// runServe starts the authenticated read-only REST mirror and runs until its
// context is canceled. Exit codes: 0 clean stop, 1 runtime error, 2 usage or
// configuration error.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String(
		"db",
		os.Getenv(databasePathEnvironment),
		"SQLite database path (default MONETA_DB_PATH)",
	)
	listenAddress := flags.String(
		"listen",
		defaultListenAddress,
		"TCP host:port to listen on (default loopback only)",
	)
	apiKeyFlag := flags.String(
		"api-key",
		"",
		"REST API key (default MONETA_API_KEY)",
	)
	allowNonLoopback := flags.Bool(
		"allow-non-loopback",
		false,
		"explicitly permit a non-loopback listen address",
	)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: serve does not accept positional arguments")
		return 2
	}
	if *databasePath == "" {
		fmt.Fprintln(stderr, "error: MONETA_DB_PATH or --db is required")
		return 2
	}
	apiKey := *apiKeyFlag
	if apiKey == "" {
		apiKey = os.Getenv(apiKeyEnvironment)
	}
	if apiKey == "" {
		fmt.Fprintln(stderr, "error: MONETA_API_KEY or --api-key is required")
		return 2
	}
	loopback, err := api.ValidateListenAddress(*listenAddress, *allowNonLoopback)
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

	logger := log.New(stderr, "", log.LstdFlags)
	handler, err := api.NewHandler(database, apiKey, logger)
	if err != nil {
		fmt.Fprintf(stderr, "error: configure REST server: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fmt.Fprintf(stderr, "error: listen on %s: %v\n", *listenAddress, err)
		return 1
	}
	defer func() { _ = listener.Close() }()

	if !loopback {
		logger.Printf(
			"WARNING: REST API is exposed without TLS on non-loopback address %s by explicit opt-in",
			listener.Addr(),
		)
	}
	logger.Printf("REST listening on %s (API-key authentication enabled)", listener.Addr())
	if err := api.Serve(ctx, listener, handler); err != nil {
		fmt.Fprintf(stderr, "error: serve REST API: %v\n", err)
		return 1
	}
	return 0
}

package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens one SQLite database, enables connection-local safety settings,
// and applies all pending schema migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// SQLite pragmas are connection-local. A single connection ensures every
	// operation uses the connection configured below and serializes writers.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	closeOnError := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(fmt.Errorf("connect to sqlite database: %w", err))
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return closeOnError(fmt.Errorf("enable sqlite foreign keys: %w", err))
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return closeOnError(fmt.Errorf("set sqlite busy timeout: %w", err))
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		return closeOnError(err)
	}

	return db, nil
}

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureDefaultEntity returns the id of the single Phase 1 personal entity,
// creating it when the database has none. Multi-entity routing is deferred.
func EnsureDefaultEntity(ctx context.Context, db *sql.DB) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("database is required")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin default entity bootstrap: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO entities (kind, name)
		SELECT 'personal', 'Personal'
		WHERE NOT EXISTS (
			SELECT 1 FROM entities WHERE kind = 'personal'
		)
		ON CONFLICT (kind, name) DO NOTHING
	`); err != nil {
		return 0, fmt.Errorf("create default entity: %w", err)
	}

	var entityID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM entities
		WHERE kind = 'personal'
		ORDER BY id
		LIMIT 1
	`).Scan(&entityID); err != nil {
		return 0, fmt.Errorf("load default entity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit default entity bootstrap: %w", err)
	}
	return entityID, nil
}

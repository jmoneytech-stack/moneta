package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DetectorState is the singleton recurring-detector lifecycle record.
// Empty timestamp strings represent SQL NULL. Item-level sync health remains
// on provider_items and is deliberately not copied here.
type DetectorState struct {
	Status              string
	LastRunAt           string
	LastSuccessAt       string
	LastError           string
	LastSeriesCount     int
	LastSkippedOverflow int
}

// ReadDetectorState returns the seeded singleton detector state. A missing row
// is treated as never_run as defense in depth for a damaged or manually edited
// database.
func ReadDetectorState(ctx context.Context, db *sql.DB) (DetectorState, error) {
	if db == nil {
		return DetectorState{}, fmt.Errorf("database is required")
	}

	var state DetectorState
	err := db.QueryRowContext(ctx, `
		SELECT
			status,
			COALESCE(last_run_at, ''),
			COALESCE(last_success_at, ''),
			last_error,
			last_series_count,
			last_skipped_overflow
		FROM detector_state
		WHERE id = 1
	`).Scan(
		&state.Status,
		&state.LastRunAt,
		&state.LastSuccessAt,
		&state.LastError,
		&state.LastSeriesCount,
		&state.LastSkippedOverflow,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DetectorState{Status: "never_run"}, nil
	}
	if err != nil {
		return DetectorState{}, fmt.Errorf("read detector state: %w", err)
	}
	return state, nil
}

// UpsertDetectorState inserts or replaces the singleton detector lifecycle
// state at id=1. Empty timestamp strings are stored as SQL NULL.
func UpsertDetectorState(ctx context.Context, db *sql.DB, state DetectorState) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	switch state.Status {
	case "never_run", "ok", "error", "partial":
	default:
		return fmt.Errorf("unsupported detector status %q", state.Status)
	}
	if state.LastSeriesCount < 0 {
		return fmt.Errorf("detector series count must not be negative")
	}
	if state.LastSkippedOverflow < 0 {
		return fmt.Errorf("detector skipped overflow count must not be negative")
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO detector_state (
			id,
			status,
			last_run_at,
			last_success_at,
			last_error,
			last_series_count,
			last_skipped_overflow
		) VALUES (1, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			status = excluded.status,
			last_run_at = excluded.last_run_at,
			last_success_at = excluded.last_success_at,
			last_error = excluded.last_error,
			last_series_count = excluded.last_series_count,
			last_skipped_overflow = excluded.last_skipped_overflow
	`,
		state.Status,
		state.LastRunAt,
		state.LastSuccessAt,
		state.LastError,
		state.LastSeriesCount,
		state.LastSkippedOverflow,
	)
	if err != nil {
		return fmt.Errorf("upsert detector state: %w", err)
	}
	return nil
}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration000005DetectorSchema(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open pre-000005 database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close pre-000005 database: %v", err)
		}
	})
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		) STRICT
	`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	for version, name := range []string{
		"000001_initial_schema.up.sql",
		"000002_import_runs_skipped.up.sql",
		"000003_normalize_liability_balance_sign.up.sql",
		"000004_merchant_display_and_card_due_date.up.sql",
	} {
		migrationSQL, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %q: %v", name, err)
		}
		if _, err := db.Exec(string(migrationSQL)); err != nil {
			t.Fatalf("apply migration %q: %v", name, err)
		}
		if _, err := db.Exec(
			"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
			version+1,
			name,
		); err != nil {
			t.Fatalf("record migration %q: %v", name, err)
		}
	}

	entityID := insertEntity(t, db, "personal", "Personal")
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source
		) VALUES (?, 'Manual Example', 'subscription', 'monthly', -1200, 'manual')
	`, entityID); err != nil {
		t.Fatalf("insert pre-000005 recurring item: %v", err)
	}

	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply migration 000005: %v", err)
	}

	columns := map[string]struct {
		declaredType string
		notNull      int
		defaultValue sql.NullString
	}{
		"detect_key":          {declaredType: "TEXT", notNull: 1, defaultValue: sql.NullString{String: "''", Valid: true}},
		"amount_sign":         {declaredType: "INTEGER", notNull: 1, defaultValue: sql.NullString{String: "0", Valid: true}},
		"miss_count":          {declaredType: "INTEGER", notNull: 1, defaultValue: sql.NullString{String: "0", Valid: true}},
		"last_matched_date":   {declaredType: "TEXT"},
		"last_matched_cents":  {declaredType: "INTEGER"},
		"schedule_anchor_day": {declaredType: "INTEGER"},
	}
	for name, want := range columns {
		var declaredType string
		var notNull int
		var defaultValue sql.NullString
		if err := db.QueryRow(`
			SELECT type, "notnull", dflt_value
			FROM pragma_table_info('recurring_items')
			WHERE name = ?
		`, name).Scan(&declaredType, &notNull, &defaultValue); err != nil {
			t.Fatalf("read recurring_items.%s info: %v", name, err)
		}
		if declaredType != want.declaredType || notNull != want.notNull ||
			defaultValue != want.defaultValue {
			t.Errorf("recurring_items.%s = type %q / notnull %d / default %#v, want %q/%d/%#v",
				name, declaredType, notNull, defaultValue,
				want.declaredType, want.notNull, want.defaultValue)
		}
	}

	var detectKey string
	var amountSign, missCount int
	var lastMatchedDate sql.NullString
	var lastMatchedCents, scheduleAnchorDay sql.NullInt64
	if err := db.QueryRow(`
		SELECT detect_key, amount_sign, miss_count, last_matched_date,
			last_matched_cents, schedule_anchor_day
		FROM recurring_items
		WHERE name = 'Manual Example'
	`).Scan(
		&detectKey,
		&amountSign,
		&missCount,
		&lastMatchedDate,
		&lastMatchedCents,
		&scheduleAnchorDay,
	); err != nil {
		t.Fatalf("read recurring item migration defaults: %v", err)
	}
	if detectKey != "" || amountSign != 0 || missCount != 0 ||
		lastMatchedDate.Valid || lastMatchedCents.Valid || scheduleAnchorDay.Valid {
		t.Errorf("migration defaults = %q/%d/%d/%v/%v/%v, want empty/0/0/NULL/NULL/NULL",
			detectKey, amountSign, missCount,
			lastMatchedDate, lastMatchedCents, scheduleAnchorDay)
	}

	for name, statement := range map[string]string{
		"amount_sign":            "UPDATE recurring_items SET amount_sign = 2",
		"miss_count":             "UPDATE recurring_items SET miss_count = -1",
		"last_matched_date":      "UPDATE recurring_items SET last_matched_date = '08/01/2026'",
		"schedule_anchor_day 0":  "UPDATE recurring_items SET schedule_anchor_day = 0",
		"schedule_anchor_day 32": "UPDATE recurring_items SET schedule_anchor_day = 32",
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Errorf("invalid %s was accepted", name)
		}
	}

	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			detect_key, amount_sign
		) VALUES (?, 'Detected Example', 'subscription', 'monthly', -1200,
			'detected', 'merchant:example', -1)
	`, entityID); err != nil {
		t.Fatalf("insert detected identity: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			detect_key, amount_sign
		) VALUES (?, 'Duplicate Detected Example', 'bill', 'monthly', -1500,
			'detected', 'merchant:example', -1)
	`, entityID); err == nil {
		t.Fatal("duplicate detected identity was accepted")
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			detect_key, amount_sign
		) VALUES (?, 'Manual Duplicate Example', 'bill', 'monthly', -1500,
			'manual', 'merchant:example', -1)
	`, entityID); err != nil {
		t.Fatalf("partial unique index rejected a manual identity: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			detect_key, amount_sign
		) VALUES (?, 'Empty Detected Identity', 'bill', 'monthly', -1500,
			'detected', '', 0)
	`, entityID); err != nil {
		t.Fatalf("partial unique index rejected an incomplete detected identity: %v", err)
	}

	var strict int
	if err := db.QueryRow(`
		SELECT strict FROM pragma_table_list WHERE name = 'detector_state'
	`).Scan(&strict); err != nil {
		t.Fatalf("read detector_state STRICT flag: %v", err)
	}
	if strict != 1 {
		t.Errorf("detector_state strict = %d, want 1", strict)
	}
	var status, lastError string
	var lastRunAt, lastSuccessAt sql.NullString
	var lastSeriesCount, lastSkippedOverflow int
	if err := db.QueryRow(`
		SELECT status, last_run_at, last_success_at, last_error,
			last_series_count, last_skipped_overflow
		FROM detector_state WHERE id = 1
	`).Scan(
		&status,
		&lastRunAt,
		&lastSuccessAt,
		&lastError,
		&lastSeriesCount,
		&lastSkippedOverflow,
	); err != nil {
		t.Fatalf("read seeded detector state: %v", err)
	}
	if status != "never_run" || lastRunAt.Valid || lastSuccessAt.Valid || lastError != "" ||
		lastSeriesCount != 0 || lastSkippedOverflow != 0 {
		t.Errorf("seeded detector state = %q/%v/%v/%q/%d/%d",
			status, lastRunAt, lastSuccessAt, lastError,
			lastSeriesCount, lastSkippedOverflow)
	}
	if _, err := db.Exec(`
		INSERT INTO detector_state (id, status) VALUES (2, 'never_run')
	`); err == nil {
		t.Fatal("detector_state accepted a second singleton id")
	}
	if _, err := db.Exec(`
		UPDATE detector_state SET last_series_count = 1.5 WHERE id = 1
	`); err == nil {
		t.Fatal("STRICT detector_state accepted a fractional count")
	}

	downSQL, err := migrationFiles.ReadFile("migrations/000005_detector_schema.down.sql")
	if err != nil {
		t.Fatalf("read migration 000005 down: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply migration 000005 down: %v", err)
	}
	for objectType, name := range map[string]string{
		"table": "detector_state",
		"index": "recurring_items_detected_identity_uidx",
	} {
		var exists bool
		if err := db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM sqlite_schema WHERE type = ? AND name = ?
			)
		`, objectType, name).Scan(&exists); err != nil {
			t.Fatalf("check %s %s removal: %v", objectType, name, err)
		}
		if exists {
			t.Errorf("%s %s still exists after down migration", objectType, name)
		}
	}
	for name := range columns {
		var exists bool
		if err := db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM pragma_table_info('recurring_items') WHERE name = ?
			)
		`, name).Scan(&exists); err != nil {
			t.Fatalf("check recurring_items.%s removal: %v", name, err)
		}
		if exists {
			t.Errorf("recurring_items.%s still exists after down migration", name)
		}
	}
	var recurringItems int
	if err := db.QueryRow("SELECT COUNT(*) FROM recurring_items").Scan(&recurringItems); err != nil {
		t.Fatalf("count recurring items after down migration: %v", err)
	}
	if recurringItems != 4 {
		t.Errorf("recurring item count after down migration = %d, want 4", recurringItems)
	}
}

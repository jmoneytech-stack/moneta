package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAppliesInitialSchemaIdempotently(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("apply migrations a second time: %v", err)
	}

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var migrationCount int
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 2 {
		t.Fatalf("migration count = %d, want 2", migrationCount)
	}

	for _, table := range []string{
		"accounts",
		"balance_snapshots",
		"budgets",
		"categories",
		"category_mappings",
		"credit_terms",
		"entities",
		"entity_rules",
		"import_runs",
		"loan_terms",
		"net_worth_snapshots",
		"provider_items",
		"recurring_items",
		"transactions",
		"txn_provider_refs",
	} {
		var exists bool
		if err := db.QueryRow(
			"SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?)",
			table,
		).Scan(&exists); err != nil {
			t.Fatalf("check table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q does not exist", table)
		}
	}
}

func TestInitialSchemaUsesIntegerMoneyColumns(t *testing.T) {
	db := openTestDB(t)

	moneyColumns := map[string][]string{
		"balance_snapshots": {
			"current_cents", "available_cents", "limit_cents",
		},
		"budgets": {
			"target_cents",
		},
		"credit_terms": {
			"limit_cents", "min_payment_cents", "last_statement_cents",
		},
		"loan_terms": {
			"min_payment_cents", "origination_cents",
		},
		"net_worth_snapshots": {
			"assets_cents", "liabilities_cents", "net_cents",
			"checking_cents", "savings_cents", "credit_card_cents",
			"loan_cents", "investment_cents", "asset_cents",
		},
		"recurring_items": {
			"expected_cents",
		},
		"transactions": {
			"amount_cents",
		},
	}

	for table, columns := range moneyColumns {
		for _, column := range columns {
			var declaredType string
			err := db.QueryRow(
				"SELECT type FROM pragma_table_info(?) WHERE name = ?",
				table,
				column,
			).Scan(&declaredType)
			if err != nil {
				t.Fatalf("read type of %s.%s: %v", table, column, err)
			}
			if declaredType != "INTEGER" {
				t.Errorf("%s.%s type = %q, want INTEGER", table, column, declaredType)
			}
		}
	}
}

func TestInitialSchemaSeedsNeutralCategories(t *testing.T) {
	db := openTestDB(t)

	var categories int
	if err := db.QueryRow("SELECT count(*) FROM categories").Scan(&categories); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	if categories != 16 {
		t.Fatalf("category count = %d, want 16", categories)
	}

	var plaidMappings int
	if err := db.QueryRow(
		"SELECT count(*) FROM category_mappings WHERE provider = 'plaid'",
	).Scan(&plaidMappings); err != nil {
		t.Fatalf("count Plaid mappings: %v", err)
	}
	if plaidMappings != 16 {
		t.Fatalf("Plaid mapping count = %d, want 16", plaidMappings)
	}
}

func TestInitialDownMigrationRemovesSchema(t *testing.T) {
	db := openTestDB(t)

	downSQL, err := migrationFiles.ReadFile("migrations/000001_initial_schema.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}

	var exists bool
	if err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM sqlite_schema
			WHERE type = 'table' AND name = 'entities'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("check schema removal: %v", err)
	}
	if exists {
		t.Fatal("entities table still exists after down migration")
	}
}

func TestImportRunsSkippedMigration(t *testing.T) {
	db := openTestDB(t)

	var declaredType string
	if err := db.QueryRow(`
		SELECT type FROM pragma_table_info('import_runs') WHERE name = 'skipped'
	`).Scan(&declaredType); err != nil {
		t.Fatalf("read import_runs.skipped type: %v", err)
	}
	if declaredType != "INTEGER" {
		t.Errorf("import_runs.skipped type = %q, want INTEGER", declaredType)
	}

	// Existing rows and fresh inserts default to zero skipped.
	if _, err := db.Exec(`
		INSERT INTO import_runs (provider, status, completed_at)
		VALUES ('plaid', 'succeeded', '2026-07-20T00:00:00.000Z')
	`); err != nil {
		t.Fatalf("insert import run: %v", err)
	}
	var skipped int
	if err := db.QueryRow("SELECT skipped FROM import_runs").Scan(&skipped); err != nil {
		t.Fatalf("read skipped default: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped default = %d, want 0", skipped)
	}

	downSQL, err := migrationFiles.ReadFile("migrations/000002_import_runs_skipped.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	var exists bool
	if err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM pragma_table_info('import_runs') WHERE name = 'skipped'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("check skipped column removal: %v", err)
	}
	if exists {
		t.Fatal("import_runs.skipped still exists after down migration")
	}
}

func TestSchemaSupportsPendingToPostedReplacement(t *testing.T) {
	db := openTestDB(t)

	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccount(t, db, entityID, "plaid-account-1")

	result, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, merchant_raw,
			merchant_norm, status, dedup_hash
		) VALUES (?, ?, '2026-07-01', -435, 'Coffee Shop', 'coffee shop', 'pending', ?)
	`, accountID, entityID, "stable-hash-without-status")
	if err != nil {
		t.Fatalf("insert pending transaction: %v", err)
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read pending transaction id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO txn_provider_refs (transaction_id, provider, provider_txn_id)
		VALUES (?, 'plaid', 'pending-transaction-id')
	`, transactionID); err != nil {
		t.Fatalf("insert pending provider reference: %v", err)
	}

	// A posted Plaid transaction carrying pending_transaction_id replaces the
	// canonical row found through the pending provider reference. The shifted
	// date and status do not create another transaction.
	if _, err := db.Exec(`
		UPDATE transactions
		SET date = '2026-07-03', status = 'posted', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = (
			SELECT transaction_id
			FROM txn_provider_refs
			WHERE provider = 'plaid' AND provider_txn_id = 'pending-transaction-id'
		)
	`); err != nil {
		t.Fatalf("replace pending transaction: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO txn_provider_refs (
			transaction_id, provider, provider_txn_id, pending_txn_id
		) VALUES (?, 'plaid', 'posted-transaction-id', 'pending-transaction-id')
	`, transactionID); err != nil {
		t.Fatalf("insert posted provider reference: %v", err)
	}

	var count int
	var date, status, dedupHash string
	if err := db.QueryRow(`
		SELECT count(*), date, status, dedup_hash
		FROM transactions
		WHERE account_id = ?
	`, accountID).Scan(&count, &date, &status, &dedupHash); err != nil {
		t.Fatalf("read replaced transaction: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction count = %d, want 1", count)
	}
	if date != "2026-07-03" || status != "posted" {
		t.Errorf("transaction date/status = %s/%s, want 2026-07-03/posted", date, status)
	}
	if dedupHash != "stable-hash-without-status" {
		t.Errorf("dedup hash changed to %q during status transition", dedupHash)
	}
}

func TestStrictSchemaRejectsFractionalCents(t *testing.T) {
	db := openTestDB(t)

	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccount(t, db, entityID, "plaid-account-1")

	_, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, status, dedup_hash
		) VALUES (?, ?, '2026-07-01', 10.5, 'posted', 'fractional-cents')
	`, accountID, entityID)
	if err == nil {
		t.Fatal("fractional cents were accepted by STRICT transactions table")
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	return db
}

func insertEntity(t *testing.T, db *sql.DB, kind, name string) int64 {
	t.Helper()

	result, err := db.Exec(
		"INSERT INTO entities (kind, name) VALUES (?, ?)",
		kind,
		name,
	)
	if err != nil {
		t.Fatalf("insert entity: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read entity id: %v", err)
	}

	return id
}

func insertAccount(t *testing.T, db *sql.DB, entityID int64, providerAccountID string) int64 {
	t.Helper()

	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, provider, provider_account_id
		) VALUES (?, 'checking', 'Test Checking', 'plaid', ?)
	`, entityID, providerAccountID)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read account id: %v", err)
	}

	return id
}

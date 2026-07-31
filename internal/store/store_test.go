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
	if migrationCount != 5 {
		t.Fatalf("migration count = %d, want 5", migrationCount)
	}

	for _, table := range []string{
		"accounts",
		"balance_snapshots",
		"budgets",
		"categories",
		"category_mappings",
		"credit_terms",
		"detector_state",
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
			"expected_cents", "last_matched_cents",
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

func TestMigration000003IsNoOpAndPreservesGenuineCredits(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open pre-000003 database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close pre-000003 database: %v", err)
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
	card := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "card-1")
	loan := insertAccountFull(t, db, entityID, "Loan Example", "loan", "loan-1")
	checking := insertAccountFull(t, db, entityID, "Checking Example", "checking", "checking-1")
	insertBalanceSnapshot(t, db, card, "2026-07-22", -5000)
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 500000)
	insertBalanceSnapshot(t, db, checking, "2026-07-22", 100000)

	if err := ApplyMigrations(context.Background(), db); err != nil {
		t.Fatalf("apply migration 000003: %v", err)
	}
	assertBalances := func(stage string) {
		t.Helper()
		for accountID, wantCents := range map[int64]int64{
			card:     -5000,
			loan:     500000,
			checking: 100000,
		} {
			var got int64
			if err := db.QueryRow(`
				SELECT current_cents
				FROM balance_snapshots
				WHERE account_id = ? AND date = '2026-07-22'
			`, accountID).Scan(&got); err != nil {
				t.Fatalf("read balance after %s: %v", stage, err)
			}
			if got != wantCents {
				t.Errorf("balance after %s = %d, want %d", stage, got, wantCents)
			}
		}
	}
	assertBalances("up migration")

	downSQL, err := migrationFiles.ReadFile(
		"migrations/000003_normalize_liability_balance_sign.down.sql",
	)
	if err != nil {
		t.Fatalf("read migration 000003 down: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply migration 000003 down: %v", err)
	}
	assertBalances("down migration")
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

func TestMigration000004MerchantDisplayAndCardDueDate(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open pre-000004 database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close pre-000004 database: %v", err)
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
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-mig4")
	if _, err := db.Exec(`
		INSERT INTO transactions (account_id, entity_id, date, amount_cents, status, dedup_hash)
		VALUES (?, ?, '2026-07-01', -100, 'posted', 'existing-mig4')
	`, accountID, entityID); err != nil {
		t.Fatalf("insert pre-000004 transaction: %v", err)
	}
	cardID := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "card-mig4")
	if _, err := db.Exec("INSERT INTO credit_terms (account_id) VALUES (?)", cardID); err != nil {
		t.Fatalf("insert pre-000004 credit terms: %v", err)
	}

	if err := ApplyMigrations(context.Background(), db); err != nil {
		t.Fatalf("apply migration 000004: %v", err)
	}

	var declaredType string
	var notNull int
	if err := db.QueryRow(`
		SELECT type, "notnull" FROM pragma_table_info('transactions')
		WHERE name = 'merchant_display'
	`).Scan(&declaredType, &notNull); err != nil {
		t.Fatalf("read merchant_display column info: %v", err)
	}
	if declaredType != "TEXT" || notNull != 1 {
		t.Errorf("merchant_display type/notnull = %q/%d, want TEXT/1", declaredType, notNull)
	}

	if err := db.QueryRow(`
		SELECT type, "notnull" FROM pragma_table_info('credit_terms')
		WHERE name = 'next_payment_due_date'
	`).Scan(&declaredType, &notNull); err != nil {
		t.Fatalf("read next_payment_due_date column info: %v", err)
	}
	if declaredType != "TEXT" || notNull != 0 {
		t.Errorf("next_payment_due_date type/notnull = %q/%d, want TEXT/0", declaredType, notNull)
	}

	// Existing and fresh rows both receive the settled defaults.
	var display string
	if err := db.QueryRow(`
		SELECT merchant_display FROM transactions WHERE dedup_hash = 'existing-mig4'
	`).Scan(&display); err != nil {
		t.Fatalf("read existing merchant_display default: %v", err)
	}
	if display != "" {
		t.Errorf("existing merchant_display = %q, want empty", display)
	}
	if _, err := db.Exec(`
		INSERT INTO transactions (account_id, entity_id, date, amount_cents, status, dedup_hash)
		VALUES (?, ?, '2026-07-02', -200, 'posted', 'fresh-mig4')
	`, accountID, entityID); err != nil {
		t.Fatalf("insert fresh transaction without merchant_display: %v", err)
	}
	if err := db.QueryRow(`
		SELECT merchant_display FROM transactions WHERE dedup_hash = 'fresh-mig4'
	`).Scan(&display); err != nil {
		t.Fatalf("read fresh merchant_display default: %v", err)
	}
	if display != "" {
		t.Errorf("fresh merchant_display = %q, want empty", display)
	}

	var dueDateNull bool
	if err := db.QueryRow(
		"SELECT next_payment_due_date IS NULL FROM credit_terms",
	).Scan(&dueDateNull); err != nil {
		t.Fatalf("read next_payment_due_date default: %v", err)
	}
	if !dueDateNull {
		t.Error("next_payment_due_date default is not NULL")
	}
	if _, err := db.Exec(`
		UPDATE credit_terms SET next_payment_due_date = '07/28/2026'
	`); err == nil {
		t.Error("invalid next_payment_due_date format accepted, want CHECK rejection")
	}

	downSQL, err := migrationFiles.ReadFile("migrations/000004_merchant_display_and_card_due_date.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	if _, err := db.Exec(string(downSQL)); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	for table, column := range map[string]string{
		"transactions": "merchant_display",
		"credit_terms": "next_payment_due_date",
	} {
		var exists bool
		if err := db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM pragma_table_info(?) WHERE name = ?
			)
		`, table, column).Scan(&exists); err != nil {
			t.Fatalf("check %s.%s removal: %v", table, column, err)
		}
		if exists {
			t.Errorf("%s.%s still exists after down migration", table, column)
		}
	}
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

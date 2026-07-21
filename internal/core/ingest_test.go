package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestApplySyncReplacesPendingWithPostedByNativeID(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	ctx := context.Background()

	pending := canon.Transaction{
		ProviderTxnID:  "pending-id",
		AccountRef:     "checking-1",
		Date:           "2026-07-01",
		AmountCents:    -435,
		MerchantRaw:    "Coffee Shop",
		SourceCategory: "FOOD_AND_DRINK",
		Status:         canon.TxnStatusPending,
		Currency:       "USD",
	}
	firstBatch := &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Institution:       "Sandbox Bank",
			Mask:              "0000",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added:      []canon.Transaction{pending},
		NextCursor: "cursor-1",
	}
	if _, err := ingestor.ApplySync(ctx, target, firstBatch); err != nil {
		t.Fatalf("apply pending batch: %v", err)
	}

	if _, err := db.Exec("UPDATE transactions SET notes = 'keep this note'"); err != nil {
		t.Fatalf("add local transaction note: %v", err)
	}

	posted := pending
	posted.ProviderTxnID = "posted-id"
	posted.PendingTxnID = "pending-id"
	posted.Date = "2026-07-03"
	posted.Status = canon.TxnStatusPosted
	target.ExpectedCursor = "cursor-1"
	secondBatch := &canon.SyncBatch{
		Added:      []canon.Transaction{posted},
		Removed:    []string{"pending-id"},
		NextCursor: "cursor-2",
	}
	if _, err := ingestor.ApplySync(ctx, target, secondBatch); err != nil {
		t.Fatalf("apply posted batch: %v", err)
	}

	var count int
	var date, status, notes string
	var amountCents int64
	if err := db.QueryRow(`
		SELECT count(*), date, status, amount_cents, notes
		FROM transactions
	`).Scan(&count, &date, &status, &amountCents, &notes); err != nil {
		t.Fatalf("read canonical transaction: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction count = %d, want 1", count)
	}
	if date != "2026-07-03" || status != "posted" {
		t.Errorf("date/status = %s/%s, want 2026-07-03/posted", date, status)
	}
	if amountCents != -435 {
		t.Errorf("amount_cents = %d, want -435", amountCents)
	}
	if notes != "keep this note" {
		t.Errorf("local notes = %q, want preserved note", notes)
	}

	var providerTransactionID, pendingTransactionID string
	if err := db.QueryRow(`
		SELECT provider_txn_id, pending_txn_id
		FROM txn_provider_refs
	`).Scan(&providerTransactionID, &pendingTransactionID); err != nil {
		t.Fatalf("read posted provider reference: %v", err)
	}
	if providerTransactionID != "posted-id" || pendingTransactionID != "pending-id" {
		t.Errorf(
			"provider reference = %s/%s, want posted-id/pending-id",
			providerTransactionID,
			pendingTransactionID,
		)
	}

	assertCursor(t, db, target.ProviderItemID, "cursor-2")
}

func TestApplySyncUsesFuzzyFallbackForIDLessPendingTransition(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "rmcsv")
	ctx := context.Background()

	account := canon.Account{
		ProviderAccountID: "checking-1",
		Name:              "Test Checking",
		Type:              canon.AccountTypeChecking,
		Currency:          "USD",
	}
	pending := canon.Transaction{
		AccountRef:  "checking-1",
		Date:        "2026-07-01",
		AmountCents: -1299,
		MerchantRaw: "Grocery Mart",
		Status:      canon.TxnStatusPending,
		Currency:    "USD",
	}
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Accounts:   []canon.Account{account},
		Added:      []canon.Transaction{pending},
		NextCursor: "cursor-1",
	}); err != nil {
		t.Fatalf("apply id-less pending batch: %v", err)
	}

	posted := pending
	posted.Date = "2026-07-03"
	posted.Status = canon.TxnStatusPosted
	target.ExpectedCursor = "cursor-1"
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Added:      []canon.Transaction{posted},
		NextCursor: "cursor-2",
	}); err != nil {
		t.Fatalf("apply id-less posted batch: %v", err)
	}

	var count int
	var date, status string
	if err := db.QueryRow(
		"SELECT count(*), date, status FROM transactions",
	).Scan(&count, &date, &status); err != nil {
		t.Fatalf("read id-less transaction: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction count = %d, want 1", count)
	}
	if date != "2026-07-03" || status != "posted" {
		t.Errorf("date/status = %s/%s, want 2026-07-03/posted", date, status)
	}
}

func TestApplySyncKeepsDistinctNativeTransactionsWithIdenticalDetails(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")

	transaction := canon.Transaction{
		ProviderTxnID: "transaction-1",
		AccountRef:    "checking-1",
		Date:          "2026-07-01",
		AmountCents:   -435,
		MerchantRaw:   "Coffee Shop",
		Status:        canon.TxnStatusPosted,
		Currency:      "USD",
	}
	secondTransaction := transaction
	secondTransaction.ProviderTxnID = "transaction-2"

	if _, err := ingestor.ApplySync(context.Background(), target, &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added:      []canon.Transaction{transaction, secondTransaction},
		NextCursor: "cursor-1",
	}); err != nil {
		t.Fatalf("apply identical native transactions: %v", err)
	}

	assertCount(t, db, "transactions", 2)
	assertCount(t, db, "txn_provider_refs", 2)
}

func TestApplySyncDoesNotFuzzyMatchPostedTransactionWithNativeID(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	ctx := context.Background()

	pending := canon.Transaction{
		ProviderTxnID: "plaid-pending-A",
		AccountRef:    "checking-1",
		Date:          "2026-07-10",
		AmountCents:   -1200,
		MerchantRaw:   "Coffee Shop",
		Status:        canon.TxnStatusPending,
		Currency:      "USD",
	}
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Institution:       "Test Bank",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added:      []canon.Transaction{pending},
		NextCursor: "cursor-1",
	}); err != nil {
		t.Fatalf("apply pending transaction: %v", err)
	}

	postedDirectly := pending
	postedDirectly.ProviderTxnID = "plaid-posted-C"
	postedDirectly.Date = "2026-07-11"
	postedDirectly.Status = canon.TxnStatusPosted
	target.ExpectedCursor = "cursor-1"
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Added:      []canon.Transaction{postedDirectly},
		NextCursor: "cursor-2",
	}); err != nil {
		t.Fatalf("apply directly posted transaction: %v", err)
	}

	postedPending := pending
	postedPending.ProviderTxnID = "plaid-posted-A"
	postedPending.PendingTxnID = "plaid-pending-A"
	postedPending.Date = "2026-07-12"
	postedPending.Status = canon.TxnStatusPosted
	target.ExpectedCursor = "cursor-2"
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Added:      []canon.Transaction{postedPending},
		NextCursor: "cursor-3",
	}); err != nil {
		t.Fatalf("apply posted pending transaction: %v", err)
	}

	var count int
	var amountCents int64
	if err := db.QueryRow(
		"SELECT count(*), sum(amount_cents) FROM transactions",
	).Scan(&count, &amountCents); err != nil {
		t.Fatalf("read canonical transactions: %v", err)
	}
	if count != 2 {
		t.Errorf("transaction count = %d, want 2", count)
	}
	if amountCents != -2400 {
		t.Errorf("amount_cents sum = %d, want -2400", amountCents)
	}
}

func TestApplySyncUpsertsBalancesLiabilitiesAndTransferCategory(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	ctx := context.Background()

	batch := &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "card-1",
			Name:              "Test Card",
			Institution:       "Sandbox Bank",
			Mask:              "1111",
			Type:              canon.AccountTypeCreditCard,
			Currency:          "usd",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID:  "payment-1",
			AccountRef:     "card-1",
			Date:           "2026-07-10",
			AmountCents:    -2500,
			MerchantRaw:    "Card Payment",
			SourceCategory: "TRANSFER_OUT",
			Status:         canon.TxnStatusPosted,
			Currency:       "USD",
		}},
		Balances: []canon.Balance{{
			AccountRef:     "card-1",
			Date:           "2026-07-10",
			CurrentCents:   32100,
			AvailableCents: 67900,
			LimitCents:     100000,
		}},
		Liabilities: []canon.Liability{{
			AccountRef:         "card-1",
			APR:                19.99,
			LimitCents:         100000,
			MinPaymentCents:    2500,
			LastStatementCents: 30000,
			StatementDay:       5,
			DueDay:             28,
		}},
		NextCursor: "cursor-1",
	}
	if _, err := ingestor.ApplySync(ctx, target, batch); err != nil {
		t.Fatalf("apply account data batch: %v", err)
	}

	var currentCents, availableCents, balanceLimitCents int64
	if err := db.QueryRow(`
		SELECT current_cents, available_cents, limit_cents
		FROM balance_snapshots
	`).Scan(&currentCents, &availableCents, &balanceLimitCents); err != nil {
		t.Fatalf("read balance snapshot: %v", err)
	}
	if currentCents != 32100 || availableCents != 67900 || balanceLimitCents != 100000 {
		t.Errorf(
			"balance cents = %d/%d/%d, want 32100/67900/100000",
			currentCents,
			availableCents,
			balanceLimitCents,
		)
	}

	var liabilityLimitCents, minimumCents, statementCents int64
	var statementDay, dueDay int
	if err := db.QueryRow(`
		SELECT limit_cents, min_payment_cents, last_statement_cents,
		       statement_day, due_day
		FROM credit_terms
	`).Scan(
		&liabilityLimitCents,
		&minimumCents,
		&statementCents,
		&statementDay,
		&dueDay,
	); err != nil {
		t.Fatalf("read credit terms: %v", err)
	}
	if liabilityLimitCents != 100000 || minimumCents != 2500 || statementCents != 30000 {
		t.Errorf(
			"credit cents = %d/%d/%d, want 100000/2500/30000",
			liabilityLimitCents,
			minimumCents,
			statementCents,
		)
	}
	if statementDay != 5 || dueDay != 28 {
		t.Errorf("statement/due day = %d/%d, want 5/28", statementDay, dueDay)
	}

	var categoryName string
	var isTransfer, excluded int
	if err := db.QueryRow(`
		SELECT categories.name, transactions.is_transfer, transactions.excluded
		FROM transactions
		JOIN categories ON categories.id = transactions.category_id
	`).Scan(&categoryName, &isTransfer, &excluded); err != nil {
		t.Fatalf("read mapped transfer: %v", err)
	}
	if categoryName != "Transfers Out" || isTransfer != 1 || excluded != 1 {
		t.Errorf(
			"mapped transfer = %q/%d/%d, want Transfers Out/1/1",
			categoryName,
			isTransfer,
			excluded,
		)
	}
}

func TestApplySyncSkipsLiabilityForUnsupportedAccountType(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	ctx := context.Background()

	result, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Liabilities: []canon.Liability{{
			AccountRef:      "checking-1",
			APR:             4.5,
			MinPaymentCents: 2500,
		}},
		NextCursor: "cursor-1",
	})
	if err != nil {
		t.Fatalf("ApplySync() error: %v", err)
	}
	if result == nil || len(result.Skipped) != 1 {
		t.Fatalf("ApplySync() skipped = %#v, want one record", result)
	}
	skipped := result.Skipped[0]
	if skipped.Kind != canon.RecordKindLiability || skipped.ID != "checking-1" ||
		skipped.Reason != canon.SkipUnsupportedAccountType ||
		skipped.Detail != string(canon.AccountTypeChecking) {
		t.Errorf("skipped record = %#v, want checking-1 liability unsupported_account_type/checking", skipped)
	}

	assertCount(t, db, "accounts", 1)
	assertCount(t, db, "credit_terms", 0)
	assertCount(t, db, "loan_terms", 0)
	assertCount(t, db, "import_runs", 1)
	assertCursor(t, db, target.ProviderItemID, "cursor-1")
}

func TestApplySyncRollsBackDataAndCursorOnFailure(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")

	_, err := ingestor.ApplySync(context.Background(), target, &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID: "transaction-1",
			AccountRef:    "missing-account",
			Date:          "2026-07-01",
			AmountCents:   -100,
			Status:        canon.TxnStatusPosted,
			Currency:      "USD",
		}},
		NextCursor: "cursor-1",
	})
	if err == nil {
		t.Fatal("ApplySync() succeeded with an unknown transaction account")
	}

	assertCount(t, db, "accounts", 0)
	assertCount(t, db, "transactions", 0)
	assertCount(t, db, "import_runs", 0)
	assertCursor(t, db, target.ProviderItemID, "")
}

func TestApplySyncRejectsStaleCursorWithoutWrites(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	target.ExpectedCursor = "stale-cursor"

	_, err := ingestor.ApplySync(context.Background(), target, &canon.SyncBatch{
		NextCursor: "new-cursor",
	})
	if !errors.Is(err, ErrCursorChanged) {
		t.Fatalf("ApplySync() error = %v, want ErrCursorChanged", err)
	}

	assertCount(t, db, "import_runs", 0)
	assertCursor(t, db, target.ProviderItemID, "")
}

func TestApplySyncRemovesProviderTransaction(t *testing.T) {
	db, ingestor, target := newTestIngestor(t, "plaid")
	ctx := context.Background()

	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID: "transaction-1",
			AccountRef:    "checking-1",
			Date:          "2026-07-01",
			AmountCents:   -100,
			Status:        canon.TxnStatusPosted,
			Currency:      "USD",
		}},
		NextCursor: "cursor-1",
	}); err != nil {
		t.Fatalf("apply transaction batch: %v", err)
	}

	target.ExpectedCursor = "cursor-1"
	if _, err := ingestor.ApplySync(ctx, target, &canon.SyncBatch{
		Removed:    []string{"transaction-1"},
		NextCursor: "cursor-2",
	}); err != nil {
		t.Fatalf("apply removal batch: %v", err)
	}

	assertCount(t, db, "transactions", 0)
	assertCount(t, db, "txn_provider_refs", 0)
	assertCursor(t, db, target.ProviderItemID, "cursor-2")
}

func newTestIngestor(t *testing.T, provider string) (*sql.DB, *Ingestor, SyncTarget) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	entityResult, err := db.Exec(
		"INSERT INTO entities (kind, name) VALUES ('personal', 'Test Personal')",
	)
	if err != nil {
		t.Fatalf("insert test entity: %v", err)
	}
	entityID, err := entityResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test entity id: %v", err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc
		) VALUES (?, 'test-item', 'Sandbox Bank', x'010203')
	`, provider)
	if err != nil {
		t.Fatalf("insert test provider item: %v", err)
	}
	providerItemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test provider item id: %v", err)
	}

	return db, NewIngestor(db), SyncTarget{
		ProviderItemID:  providerItemID,
		DefaultEntityID: entityID,
		ExpectedCursor:  "",
	}
}

func assertCursor(t *testing.T, db *sql.DB, providerItemID int64, want string) {
	t.Helper()

	var got string
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		providerItemID,
	).Scan(&got); err != nil {
		t.Fatalf("read provider item cursor: %v", err)
	}
	if got != want {
		t.Errorf("sync cursor = %q, want %q", got, want)
	}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()

	var got int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Errorf("%s count = %d, want %d", table, got, want)
	}
}

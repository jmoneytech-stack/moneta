package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

func insertSpendTransaction(
	t *testing.T,
	db *sql.DB,
	accountID int64,
	entityID int64,
	date string,
	amountCents int64,
	merchant string,
	categoryID any,
	status string,
	excluded int,
	hash string,
) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, merchant_raw,
			merchant_norm, category_id, status, excluded, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, accountID, entityID, date, amountCents, merchant, merchant,
		categoryID, status, excluded, hash); err != nil {
		t.Fatalf("insert spend transaction %q: %v", hash, err)
	}
}

func TestReadSpendFiltersAnalyticsRowsAndGroups(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")

	// Inclusive bounds and valid posted outflows.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-01", -2000, "Grocery Mart", int64(7), "posted", 0, "spend-food")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-31", -500, "Cafe Example", nil, "posted", 0, "spend-cafe")
	// Not spend: inflow, pending, excluded transfer, and outside the period.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-15", 100000, "Employer Example", int64(1), "posted", 0, "income")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-16", -3000, "Pending Shop", int64(7), "pending", 0, "pending")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-17", -500000, "Transfer Example", int64(2), "posted", 1, "transfer")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-30", -1000, "Outside Shop", int64(7), "posted", 0, "outside")

	report, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01",
		To:   "2026-07-31",
	}, 20)
	if err != nil {
		t.Fatalf("ReadSpend() error: %v", err)
	}
	if report.Summary.Count != 2 || report.Summary.SpendCents != 2500 {
		t.Errorf("summary = %+v, want count 2 / spend 2500", report.Summary)
	}
	if report.CategoryTotal != 2 || len(report.Categories) != 2 {
		t.Fatalf("categories = %d/%d, want 2/2: %+v",
			len(report.Categories), report.CategoryTotal, report.Categories)
	}
	if report.Categories[0] != (SpendGroup{Name: "Food and Drink", Count: 1, SpendCents: 2000}) {
		t.Errorf("categories[0] = %+v", report.Categories[0])
	}
	if report.Categories[1] != (SpendGroup{Name: "Uncategorized", Count: 1, SpendCents: 500}) {
		t.Errorf("categories[1] = %+v", report.Categories[1])
	}
	if report.MerchantTotal != 2 || len(report.Merchants) != 2 {
		t.Fatalf("merchants = %d/%d, want 2/2: %+v",
			len(report.Merchants), report.MerchantTotal, report.Merchants)
	}
	if report.Merchants[0] != (SpendGroup{Name: "Grocery Mart", Count: 1, SpendCents: 2000}) {
		t.Errorf("merchants[0] = %+v", report.Merchants[0])
	}
}

func TestReadSpendKeepsSameNamedCategoriesSeparate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")

	rootResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('Coffee', 'expense')
	`)
	if err != nil {
		t.Fatalf("insert root category: %v", err)
	}
	rootID, err := rootResult.LastInsertId()
	if err != nil {
		t.Fatalf("root category id: %v", err)
	}
	parentResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('Food Parent', 'expense')
	`)
	if err != nil {
		t.Fatalf("insert parent category: %v", err)
	}
	parentID, err := parentResult.LastInsertId()
	if err != nil {
		t.Fatalf("parent category id: %v", err)
	}
	childResult, err := db.Exec(`
		INSERT INTO categories (name, parent_id, kind) VALUES ('Coffee', ?, 'expense')
	`, parentID)
	if err != nil {
		t.Fatalf("insert child category: %v", err)
	}
	childID, err := childResult.LastInsertId()
	if err != nil {
		t.Fatalf("child category id: %v", err)
	}

	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-10", -200, "Root Cafe", rootID, "posted", 0, "root-coffee")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-11", -100, "Child Cafe One", childID, "posted", 0, "child-coffee-1")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-12", -100, "Child Cafe Two", childID, "posted", 0, "child-coffee-2")

	report, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01", To: "2026-07-31",
	}, 20)
	if err != nil {
		t.Fatalf("ReadSpend() error: %v", err)
	}
	if report.Summary.Count != 3 || report.Summary.SpendCents != 400 {
		t.Errorf("summary = %+v, want count 3 / spend 400", report.Summary)
	}
	if report.CategoryTotal != 2 || len(report.Categories) != 2 {
		t.Fatalf("categories = %d/%d, want 2/2: %+v",
			len(report.Categories), report.CategoryTotal, report.Categories)
	}
	want := []SpendGroup{
		{Name: "Coffee", Count: 1, SpendCents: 200},
		{Name: "Coffee", Count: 2, SpendCents: 200},
	}
	for i := range want {
		if report.Categories[i] != want[i] {
			t.Errorf("categories[%d] = %+v, want %+v", i, report.Categories[i], want[i])
		}
	}
}

func TestReadSpendAccountFilterIsLiteral(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	percentID := insertAccountFull(t, db, entityID, "Percent_Account", "checking", "acct-1")
	otherID := insertAccountFull(t, db, entityID, "Other Account", "checking", "acct-2")
	insertSpendTransaction(t, db, percentID, entityID,
		"2026-07-10", -100, "One Shop", nil, "posted", 0, "percent")
	insertSpendTransaction(t, db, otherID, entityID,
		"2026-07-10", -200, "Other Shop", nil, "posted", 0, "other")

	report, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01", To: "2026-07-31", Account: "_",
	}, 20)
	if err != nil {
		t.Fatalf("ReadSpend() error: %v", err)
	}
	if report.Summary.Count != 1 || report.Summary.SpendCents != 100 {
		t.Errorf("summary = %+v, want only the literal-underscore account", report.Summary)
	}
}

func TestReadSpendEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	report, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01", To: "2026-07-31",
	}, 20)
	if err != nil {
		t.Fatalf("ReadSpend() error: %v", err)
	}
	if report.Summary != (SpendSummary{}) || report.CategoryTotal != 0 || report.MerchantTotal != 0 {
		t.Errorf("empty report = %+v", report)
	}

	filters := []SpendFilter{
		{},
		{From: "2026-07-01"},
		{From: "bad", To: "2026-07-31"},
		{From: "2026-07-31", To: "2026-07-01"},
	}
	for _, filter := range filters {
		if _, err := ReadSpend(ctx, db, filter, 20); err == nil {
			t.Errorf("ReadSpend(%+v) succeeded, want an error", filter)
		}
	}
	if _, err := ReadSpend(ctx, nil, SpendFilter{
		From: "2026-07-01", To: "2026-07-31",
	}, 20); err == nil {
		t.Error("ReadSpend(nil db) succeeded, want an error")
	}
}

func TestReadSpendTruncatesGroupsButNotSummary(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")
	for i := 0; i < 25; i++ {
		insertSpendTransaction(t, db, accountID, entityID,
			"2026-07-10", int64(-100-i), fmt.Sprintf("Merchant %02d", i),
			nil, "posted", 0, fmt.Sprintf("spend-%02d", i))
	}

	report, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01", To: "2026-07-31",
	}, 20)
	if err != nil {
		t.Fatalf("ReadSpend() error: %v", err)
	}
	if report.Summary.Count != 25 {
		t.Errorf("summary count = %d, want 25", report.Summary.Count)
	}
	if report.MerchantTotal != 25 || len(report.Merchants) != 20 {
		t.Errorf("merchant groups = %d/%d, want 20/25",
			len(report.Merchants), report.MerchantTotal)
	}
	if report.CategoryTotal != 1 || len(report.Categories) != 1 {
		t.Errorf("category groups = %d/%d, want 1/1",
			len(report.Categories), report.CategoryTotal)
	}

	full, err := ReadSpend(ctx, db, SpendFilter{
		From: "2026-07-01", To: "2026-07-31",
	}, 0)
	if err != nil {
		t.Fatalf("ReadSpend(full) error: %v", err)
	}
	if len(full.Merchants) != 25 {
		t.Errorf("full merchants = %d, want 25", len(full.Merchants))
	}
}

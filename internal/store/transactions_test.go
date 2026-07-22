package store

import (
	"context"
	"database/sql"
	"testing"
)

func insertTransaction(
	t *testing.T,
	db *sql.DB,
	accountID int64,
	entityID int64,
	date string,
	amountCents int64,
	merchantRaw string,
	merchantNorm string,
	status string,
	hash string,
) {
	t.Helper()

	if _, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents,
			merchant_raw, merchant_norm, status, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, accountID, entityID, date, amountCents, merchantRaw, merchantNorm, status, hash); err != nil {
		t.Fatalf("insert transaction %q: %v", hash, err)
	}
}

// seedTransactionFixture builds two accounts with a small transaction set:
//
//	checking: -500 Grocery Mart (posted 07-20), -1550 Stream Co (pending 07-19),
//	          +250000 Employer Inc (posted 07-01)
//	card:     -4218 Coffee Place (posted 07-18)
func seedTransactionFixture(t *testing.T, db *sql.DB) (checkingID, cardID, entityID int64) {
	t.Helper()

	ctx := context.Background()
	var err error
	entityID, err = EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	checkingID = insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")
	cardID = insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "acct-2")

	insertTransaction(t, db, checkingID, entityID,
		"2026-07-20", -500, "GROCERY MART #42", "Grocery Mart", "posted", "hash-1")
	insertTransaction(t, db, checkingID, entityID,
		"2026-07-19", -1550, "STREAMCO", "Stream Co", "pending", "hash-2")
	insertTransaction(t, db, checkingID, entityID,
		"2026-07-01", 250000, "EMPLOYER INC PAYROLL", "Employer Inc", "posted", "hash-3")
	insertTransaction(t, db, cardID, entityID,
		"2026-07-18", -4218, "COFFEE PLACE", "Coffee Place", "posted", "hash-4")
	return checkingID, cardID, entityID
}

func TestSummarizeTransactionsAggregates(t *testing.T) {
	db := openTestDB(t)
	seedTransactionFixture(t, db)

	summary, err := SummarizeTransactions(context.Background(), db, TransactionFilter{})
	if err != nil {
		t.Fatalf("SummarizeTransactions() error: %v", err)
	}
	if summary.Count != 4 {
		t.Errorf("Count = %d, want 4", summary.Count)
	}
	if summary.TotalCents != 243732 {
		t.Errorf("TotalCents = %d, want 243732", summary.TotalCents)
	}
	if summary.InflowCents != 250000 {
		t.Errorf("InflowCents = %d, want 250000", summary.InflowCents)
	}
	if summary.OutflowCents != -6268 {
		t.Errorf("OutflowCents = %d, want -6268", summary.OutflowCents)
	}
}

// TestSummarizeTransactionsExcludesTransferAndExcludedRows pins DP2: the
// aggregate ignores excluded rows (transfers), while the count and the row
// listing still cover every match.
func TestSummarizeTransactionsExcludesTransferAndExcludedRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")

	insertExcluded := func(date string, amountCents int64, merchant, hash string, excluded int) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents,
				merchant_raw, merchant_norm, status, excluded, dedup_hash
			) VALUES (?, ?, ?, ?, ?, ?, 'posted', ?, ?)
		`, accountID, entityID, date, amountCents, merchant, merchant, excluded, hash); err != nil {
			t.Fatalf("insert transaction %q: %v", hash, err)
		}
	}
	insertExcluded("2026-07-20", 500000, "Transfer In", "hash-tin", 1)
	insertExcluded("2026-07-19", -500000, "Transfer Out", "hash-tout", 1)
	insertExcluded("2026-07-18", -2000, "Grocery Mart", "hash-buy", 0)

	filter := TransactionFilter{}
	summary, err := SummarizeTransactions(ctx, db, filter)
	if err != nil {
		t.Fatalf("SummarizeTransactions() error: %v", err)
	}
	if summary.Count != 3 {
		t.Errorf("Count = %d, want 3 (all matching rows)", summary.Count)
	}
	if summary.ExcludedCount != 2 {
		t.Errorf("ExcludedCount = %d, want 2", summary.ExcludedCount)
	}
	if summary.TotalCents != -2000 {
		t.Errorf("TotalCents = %d, want -2000 (transfers excluded)", summary.TotalCents)
	}
	if summary.InflowCents != 0 {
		t.Errorf("InflowCents = %d, want 0 (transfer-in excluded)", summary.InflowCents)
	}
	if summary.OutflowCents != -2000 {
		t.Errorf("OutflowCents = %d, want -2000 (transfer-out excluded)", summary.OutflowCents)
	}

	// The listing still shows every matching row.
	transactions, err := ListTransactions(ctx, db, filter, 0)
	if err != nil {
		t.Fatalf("ListTransactions() error: %v", err)
	}
	if len(transactions) != 3 {
		t.Errorf("ListTransactions() = %d rows, want all 3 including transfers", len(transactions))
	}
}

func TestSummarizeTransactionsEmpty(t *testing.T) {
	db := openTestDB(t)

	summary, err := SummarizeTransactions(context.Background(), db, TransactionFilter{})
	if err != nil {
		t.Fatalf("SummarizeTransactions() error: %v", err)
	}
	if summary != (TransactionSummary{}) {
		t.Errorf("SummarizeTransactions() = %+v, want zero summary", summary)
	}
}

func TestListTransactionsOrderAndLimit(t *testing.T) {
	db := openTestDB(t)
	seedTransactionFixture(t, db)

	transactions, err := ListTransactions(context.Background(), db, TransactionFilter{}, 2)
	if err != nil {
		t.Fatalf("ListTransactions() error: %v", err)
	}
	if len(transactions) != 2 {
		t.Fatalf("ListTransactions(limit 2) = %d rows, want 2", len(transactions))
	}
	if transactions[0].Date != "2026-07-20" || transactions[0].Merchant != "Grocery Mart" {
		t.Errorf("first row = %+v, want 2026-07-20 Grocery Mart", transactions[0])
	}
	if transactions[0].AccountName != "Everyday Checking" || transactions[0].Status != "posted" {
		t.Errorf("first row context = %q / %q", transactions[0].AccountName, transactions[0].Status)
	}
	if transactions[1].Date != "2026-07-19" {
		t.Errorf("second row date = %q, want 2026-07-19", transactions[1].Date)
	}

	all, err := ListTransactions(context.Background(), db, TransactionFilter{}, 0)
	if err != nil {
		t.Fatalf("ListTransactions() error: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("ListTransactions(no limit) = %d rows, want 4", len(all))
	}
}

func TestListTransactionsMerchantFallbackToRaw(t *testing.T) {
	db := openTestDB(t)
	_, _, entityID := seedTransactionFixture(t, db)
	accountID := insertAccountFull(t, db, entityID, "Spare", "savings", "acct-3")
	insertTransaction(t, db, accountID, entityID,
		"2026-07-21", -100, "RAW ONLY MERCHANT", "", "posted", "hash-raw")

	transactions, err := ListTransactions(
		context.Background(), db, TransactionFilter{Search: "raw only"}, 0)
	if err != nil {
		t.Fatalf("ListTransactions() error: %v", err)
	}
	if len(transactions) != 1 || transactions[0].Merchant != "RAW ONLY MERCHANT" {
		t.Errorf("ListTransactions() = %+v, want raw merchant fallback", transactions)
	}
}

func TestTransactionFilters(t *testing.T) {
	db := openTestDB(t)
	seedTransactionFixture(t, db)
	ctx := context.Background()

	tests := []struct {
		name      string
		filter    TransactionFilter
		wantCount int
	}{
		{"from bound", TransactionFilter{From: "2026-07-19"}, 2},
		{"to bound", TransactionFilter{To: "2026-07-18"}, 2},
		{"date range", TransactionFilter{From: "2026-07-18", To: "2026-07-19"}, 2},
		{"account substring case-insensitive", TransactionFilter{Account: "travel"}, 1},
		{"merchant search normalized", TransactionFilter{Search: "grocery"}, 1},
		{"merchant search raw", TransactionFilter{Search: "streamco"}, 1},
		{"combined", TransactionFilter{From: "2026-07-01", To: "2026-07-20", Account: "checking", Search: "stream"}, 1},
		{"no match", TransactionFilter{Search: "nonexistent"}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, err := SummarizeTransactions(ctx, db, test.filter)
			if err != nil {
				t.Fatalf("SummarizeTransactions() error: %v", err)
			}
			if summary.Count != test.wantCount {
				t.Errorf("Count = %d, want %d", summary.Count, test.wantCount)
			}
			transactions, err := ListTransactions(ctx, db, test.filter, 0)
			if err != nil {
				t.Fatalf("ListTransactions() error: %v", err)
			}
			if len(transactions) != test.wantCount {
				t.Errorf("ListTransactions() = %d rows, want %d", len(transactions), test.wantCount)
			}
		})
	}
}

func TestTransactionFiltersEscapeLikeMetacharacters(t *testing.T) {
	db := openTestDB(t)
	_, _, entityID := seedTransactionFixture(t, db)
	accountID := insertAccountFull(t, db, entityID, "Percent_Account", "savings", "acct-9")
	insertTransaction(t, db, accountID, entityID,
		"2026-07-21", -100, "100% REAL DEAL", "100% Real Deal", "posted", "hash-pct")
	ctx := context.Background()

	tests := []struct {
		name      string
		filter    TransactionFilter
		wantCount int
	}{
		// A bare % must match only rows containing a literal percent sign,
		// not every row (4 fixture + 1 here without escaping).
		{"percent literal", TransactionFilter{Search: "100%"}, 1},
		{"bare percent is not a wildcard", TransactionFilter{Search: "%"}, 1},
		{"underscore literal", TransactionFilter{Search: "real deal"}, 1},
		{"account underscore literal", TransactionFilter{Account: "percent_account"}, 1},
		{"account bare underscore is not a wildcard", TransactionFilter{Account: "_"}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, err := SummarizeTransactions(ctx, db, test.filter)
			if err != nil {
				t.Fatalf("SummarizeTransactions() error: %v", err)
			}
			if summary.Count != test.wantCount {
				t.Errorf("Count = %d, want %d", summary.Count, test.wantCount)
			}
		})
	}
}

// TestTransactionReadsValidateFilter pins store-side defense-in-depth: the
// store rejects malformed or inverted date bounds itself instead of relying
// on the CLI caller to validate first.
func TestTransactionReadsValidateFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	filters := []TransactionFilter{
		{From: "not-a-date"},
		{To: "2026-02-30"},
		{From: "2026-07-20", To: "2026-07-01"},
	}
	for _, filter := range filters {
		if _, err := SummarizeTransactions(ctx, db, filter); err == nil {
			t.Errorf("SummarizeTransactions(%+v) succeeded, want an error", filter)
		}
		if _, err := ListTransactions(ctx, db, filter, 0); err == nil {
			t.Errorf("ListTransactions(%+v) succeeded, want an error", filter)
		}
	}
}

func TestTransactionReadsRequireDB(t *testing.T) {
	if _, err := SummarizeTransactions(context.Background(), nil, TransactionFilter{}); err == nil {
		t.Fatal("SummarizeTransactions(nil) succeeded, want an error")
	}
	if _, err := ListTransactions(context.Background(), nil, TransactionFilter{}, 0); err == nil {
		t.Fatal("ListTransactions(nil) succeeded, want an error")
	}
}

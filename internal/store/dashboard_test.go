package store

import (
	"context"
	"database/sql"
	"testing"
)

func seedDashboardDB(t *testing.T, db *sql.DB) {
	t.Helper()
	entityID := insertEntity(t, db, "personal", "Personal")

	checking := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "chk-1")
	savings := insertAccountFull(t, db, entityID, "Rainy Day", "savings", "sav-1")
	travel := insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "card-1")
	storeCard := insertAccountFull(t, db, entityID, "Store Card", "credit_card", "card-2")
	loan := insertAccountFull(t, db, entityID, "Auto Loan", "loan", "loan-1")

	insertBalanceSnapshot(t, db, checking, "2026-07-22", 120000)
	insertBalanceSnapshot(t, db, savings, "2026-07-22", 50000)
	insertBalanceSnapshot(t, db, travel, "2026-07-22", 340000)
	insertBalanceSnapshot(t, db, storeCard, "2026-07-22", 15000)
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 500000)
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, NULL, 27.49, 3)
	`, travel, storeCard); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}

	insertSpendTransaction(t, db, checking, entityID,
		"2026-07-10", -2500, "Coffee Example", int64(7), "posted", 0, "dash-spend")
	insertSpendTransaction(t, db, checking, entityID,
		"2026-07-01", 100000, "Payroll Example", int64(1), "posted", 0, "dash-inflow")
	// Outside the month, excluded, and pending rows must not move any total.
	insertSpendTransaction(t, db, checking, entityID,
		"2026-06-30", -900000, "Old Example", int64(7), "posted", 0, "dash-outside")
	insertSpendTransaction(t, db, checking, entityID,
		"2026-07-11", -800000, "Excluded Example", int64(7), "posted", 1, "dash-excluded")
	insertSpendTransaction(t, db, checking, entityID,
		"2026-07-12", -700000, "Pending Example", int64(7), "pending", 0, "dash-pending")
}

func insertDashboardProviderItem(t *testing.T, db *sql.DB, itemID, status string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc, status, last_synced_at
		) VALUES ('plaid', ?, 'Fake Bank', ?, ?, '2026-07-22T12:00:00Z')
	`, itemID, []byte("encrypted-test-placeholder"), status); err != nil {
		t.Fatalf("insert provider item %q: %v", itemID, err)
	}
}

func TestReadDashboardComposesStoreReads(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedDashboardDB(t, db)
	insertDashboardProviderItem(t, db, "item-ok", "ok")
	insertDashboardProviderItem(t, db, "item-stale", "login_required")

	report, err := ReadDashboard(ctx, db, DashboardFilter{
		From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
	})
	if err != nil {
		t.Fatalf("ReadDashboard() error: %v", err)
	}

	if report.AsOf != "2026-07-22" {
		t.Errorf("AsOf = %q, want 2026-07-22", report.AsOf)
	}
	if report.Networth.AssetsCents != 170000 ||
		report.Networth.LiabilitiesCents != 855000 ||
		report.Networth.NetworthCents != -685000 {
		t.Errorf("networth = %+v, want assets 170000, liabilities 855000, net -685000", report.Networth)
	}
	if report.CashCents != 170000 || report.CashAccounts != 2 {
		t.Errorf("cash = %d over %d accounts, want 170000 over 2",
			report.CashCents, report.CashAccounts)
	}
	if report.CardCount != 2 || report.CardDebtCents != 355000 {
		t.Errorf("cards = %d cards owing %d, want 2 owing 355000",
			report.CardCount, report.CardDebtCents)
	}
	// Only Travel Card has both a balance and a positive limit.
	if report.UtilizationCards != 1 ||
		report.UtilizationDebtCents != 340000 || report.UtilizationLimitCents != 1000000 {
		t.Errorf("utilization inputs = %d cards, %d debt, %d limit, want 1, 340000, 1000000",
			report.UtilizationCards, report.UtilizationDebtCents, report.UtilizationLimitCents)
	}
	if report.Spend.SpendCents != 2500 || report.Spend.Count != 1 {
		t.Errorf("spend = %+v, want 2500 over 1 transaction", report.Spend)
	}
	if report.Cashflow.InflowCents != 100000 || report.Cashflow.OutflowCents != 2500 ||
		report.Cashflow.NetCents != 97500 {
		t.Errorf("cashflow = %+v, want inflow 100000, outflow 2500, net 97500", report.Cashflow)
	}
	if report.Sync.Items != 2 || report.Sync.NeedsAttention != 1 || report.Sync.LoginRequired != 1 {
		t.Errorf("sync = %+v, want 2 items, 1 needing attention, 1 login_required", report.Sync)
	}
	if report.From != "2026-07-01" || report.To != "2026-07-31" {
		t.Errorf("period = %q..%q, want 2026-07-01..2026-07-31", report.From, report.To)
	}
}

func TestReadDashboardUtilizationFloorsOverpayAndPrefersSnapshotLimit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	overpayID := insertAccountFull(t, db, entityID, "Overpaid Card", "credit_card", "card-overpay")
	dualLimitID := insertAccountFull(t, db, entityID, "Dual Limit Card", "credit_card", "card-dual")
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents) VALUES (?, 10000), (?, 1000000)
	`, overpayID, dualLimitID); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	insertUtilizationSnapshot(t, db, overpayID, "2026-07-22", -5000, nil)
	insertUtilizationSnapshot(t, db, dualLimitID, "2026-07-22", 200000, int64(800000))

	report, err := ReadDashboard(ctx, db, DashboardFilter{
		From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
	})
	if err != nil {
		t.Fatalf("ReadDashboard() error: %v", err)
	}
	// The overpaid card floors to zero debt, and the dual-limit card uses its
	// snapshot limit rather than credit_terms.
	if report.UtilizationCards != 2 ||
		report.UtilizationDebtCents != 200000 || report.UtilizationLimitCents != 810000 {
		t.Errorf("utilization inputs = %d cards, %d debt, %d limit, want 2, 200000, 810000",
			report.UtilizationCards, report.UtilizationDebtCents, report.UtilizationLimitCents)
	}
	// Signed card debt is unchanged: the overpay credit still reduces what is owed.
	if report.CardCount != 2 || report.CardDebtCents != 195000 {
		t.Errorf("cards = %d owing %d, want 2 owing 195000", report.CardCount, report.CardDebtCents)
	}
}

func TestReadDashboardEmptyDatabaseAndValidation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	filter := DashboardFilter{
		From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
	}

	report, err := ReadDashboard(ctx, db, filter)
	if err != nil {
		t.Fatalf("ReadDashboard(empty) error: %v", err)
	}
	if report.AsOf != "" || report.CashCents != 0 || report.CashAccounts != 0 ||
		report.CardCount != 0 || report.UtilizationCards != 0 ||
		report.Spend.Count != 0 || report.Cashflow.Count != 0 || report.Sync.Items != 0 {
		t.Errorf("empty report = %+v, want zero-valued sections", report)
	}
	if report.From != "2026-07-01" || report.To != "2026-07-31" {
		t.Errorf("empty report echoes period %q..%q", report.From, report.To)
	}

	if _, err := ReadDashboard(ctx, nil, filter); err == nil {
		t.Error("ReadDashboard(nil db) succeeded, want error")
	}
	for _, bad := range []DashboardFilter{
		{},
		{From: "bad", To: "2026-07-31"},
		{From: "2026-08-01", To: "2026-07-31"},
	} {
		if _, err := ReadDashboard(ctx, db, bad); err == nil {
			t.Errorf("ReadDashboard(%+v) succeeded, want error", bad)
		}
	}
}

func TestReadDashboardUtilizationSkipsUnusableLimits(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	zeroLimit := insertAccountFull(t, db, entityID, "Zero Limit Card", "credit_card", "card-1")
	noSnapshot := insertAccountFull(t, db, entityID, "Pending Card", "credit_card", "card-2")
	insertBalanceSnapshot(t, db, zeroLimit, "2026-07-22", 5000)
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents) VALUES (?, 0), (?, 900000)
	`, zeroLimit, noSnapshot); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}

	report, err := ReadDashboard(ctx, db, DashboardFilter{
		From: "2026-07-01", To: "2026-07-31", BillsAsOf: "2026-07-01",
	})
	if err != nil {
		t.Fatalf("ReadDashboard() error: %v", err)
	}
	// A zero limit and a card with no balance both leave utilization undefined.
	if report.UtilizationCards != 0 ||
		report.UtilizationDebtCents != 0 || report.UtilizationLimitCents != 0 {
		t.Errorf("utilization inputs = %d cards, %d debt, %d limit, want all zero",
			report.UtilizationCards, report.UtilizationDebtCents, report.UtilizationLimitCents)
	}
	if report.CardCount != 2 || report.CardDebtCents != 5000 {
		t.Errorf("cards = %d owing %d, want 2 owing 5000", report.CardCount, report.CardDebtCents)
	}
}

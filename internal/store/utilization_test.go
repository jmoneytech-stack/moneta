package store

import (
	"context"
	"testing"
)

func TestReadPortfolioUtilizationFloorsOverpayAndPrefersSnapshotLimit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	overpayID := insertAccountFull(t, db, entityID, "Overpaid Card", "credit_card", "card-overpay")
	dualLimitID := insertAccountFull(t, db, entityID, "Dual Limit Card", "credit_card", "card-dual")
	noLimitID := insertAccountFull(t, db, entityID, "No Limit Card", "credit_card", "card-nolimit")
	noSnapshotID := insertAccountFull(t, db, entityID, "Pending Card", "credit_card", "card-pending")
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents) VALUES
			(?, 10000),
			(?, 1000000),
			(?, 0),
			(?, 900000)
	`, overpayID, dualLimitID, noLimitID, noSnapshotID); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}

	// Overpaid balance contributes zero debt; the NULL snapshot limit falls
	// back to credit_terms.
	insertUtilizationSnapshot(t, db, overpayID, "2026-07-22", -5000, nil)
	// The latest snapshot limit wins over credit_terms; the older snapshot's
	// limit must not leak into the current portfolio.
	insertUtilizationSnapshot(t, db, dualLimitID, "2026-07-01", 100000, int64(500000))
	insertUtilizationSnapshot(t, db, dualLimitID, "2026-07-22", 200000, int64(800000))
	// No positive usable limit: excluded.
	insertUtilizationSnapshot(t, db, noLimitID, "2026-07-22", 25000, nil)
	// noSnapshotID has terms but no balance snapshot: excluded.

	portfolio, err := ReadPortfolioUtilization(ctx, db)
	if err != nil {
		t.Fatalf("ReadPortfolioUtilization() error: %v", err)
	}
	want := PortfolioUtilization{
		Cards:      2,
		DebtCents:  200000,
		LimitCents: 810000,
	}
	if portfolio != want {
		t.Errorf("portfolio = %+v, want %+v", portfolio, want)
	}
}

func TestReadPortfolioUtilizationEmptyAndNilDB(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	portfolio, err := ReadPortfolioUtilization(ctx, db)
	if err != nil {
		t.Fatalf("ReadPortfolioUtilization(empty) error: %v", err)
	}
	if portfolio != (PortfolioUtilization{}) {
		t.Errorf("empty portfolio = %+v, want zero report", portfolio)
	}
	if _, err := ReadPortfolioUtilization(ctx, nil); err == nil {
		t.Error("ReadPortfolioUtilization(nil db) succeeded, want error")
	}
}

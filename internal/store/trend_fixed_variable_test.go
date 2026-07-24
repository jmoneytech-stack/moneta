package store

import (
	"context"
	"testing"
)

func TestReadTrendFixedVariableClassifiesSpendAndExcludesNonSpend(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-fixed-variable")

	// Exercise the stable-name fallback independently from the seeded ID 16.
	nameFallbackResult, err := db.Exec(`
		INSERT INTO categories (name, parent_id, kind)
		VALUES ('Rent and Utilities', 7, 'expense')
	`)
	if err != nil {
		t.Fatalf("insert fixed-name fallback category: %v", err)
	}
	nameFallbackID, err := nameFallbackResult.LastInsertId()
	if err != nil {
		t.Fatalf("fixed-name fallback category id: %v", err)
	}

	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-02", -5000, "Rent Example", int64(16), "posted", 0, "fixed-id")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-03", -2000, "Utility Example", nameFallbackID, "posted", 0, "fixed-name")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-04", -3000, "Food Example", int64(7), "posted", 0, "variable")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-05", -1000, "Unknown Example", nil, "posted", 0, "unclassified")

	// These rows must not affect any bucket or total.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-06", -900000, "Excluded Transfer", int64(2), "posted", 1, "excluded-transfer")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-07", -800000, "Pending Rent", int64(16), "pending", 0, "pending")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-08", 700000, "Income Example", int64(1), "posted", 0, "income")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-30", -600000, "Outside Food", int64(7), "posted", 0, "outside")

	filter := TrendFixedVariableFilter{From: "2026-07-01", To: "2026-07-31"}
	report, err := ReadTrendFixedVariable(ctx, db, filter)
	if err != nil {
		t.Fatalf("ReadTrendFixedVariable() error: %v", err)
	}
	want := TrendFixedVariableReport{
		Fixed:        TrendFixedVariableBucket{SpendCents: 7000, Count: 2},
		Variable:     TrendFixedVariableBucket{SpendCents: 3000, Count: 1},
		Unclassified: TrendFixedVariableBucket{SpendCents: 1000, Count: 1},
		TotalCents:   11000,
		Count:        4,
	}
	if report != want {
		t.Errorf("report = %+v, want %+v", report, want)
	}
}

func TestReadTrendFixedVariableEmptyValidatesAndFiltersAccountsLiterally(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	valid := TrendFixedVariableFilter{From: "2026-07-01", To: "2026-07-31"}
	report, err := ReadTrendFixedVariable(ctx, db, valid)
	if err != nil {
		t.Fatalf("ReadTrendFixedVariable(empty) error: %v", err)
	}
	if report != (TrendFixedVariableReport{}) {
		t.Errorf("empty report = %+v, want zero report", report)
	}

	entityID := insertEntity(t, db, "personal", "Personal")
	underscoreID := insertAccountFull(t, db, entityID, "Percent_Account", "checking", "acct-fixed-1")
	otherID := insertAccountFull(t, db, entityID, "PercentXAccount", "checking", "acct-fixed-2")
	insertSpendTransaction(t, db, underscoreID, entityID,
		"2026-07-10", -100, "Literal Match", int64(16), "posted", 0, "fixed-literal")
	insertSpendTransaction(t, db, otherID, entityID,
		"2026-07-10", -200, "Wildcard Match", int64(7), "posted", 0, "fixed-wildcard")

	report, err = ReadTrendFixedVariable(ctx, db, TrendFixedVariableFilter{
		From: "2026-07-01", To: "2026-07-31", Account: "_",
	})
	if err != nil {
		t.Fatalf("ReadTrendFixedVariable(account) error: %v", err)
	}
	want := TrendFixedVariableReport{
		Fixed:      TrendFixedVariableBucket{SpendCents: 100, Count: 1},
		TotalCents: 100,
		Count:      1,
	}
	if report != want {
		t.Errorf("filtered report = %+v, want %+v", report, want)
	}

	if _, err := ReadTrendFixedVariable(ctx, nil, valid); err == nil {
		t.Error("ReadTrendFixedVariable(nil db) succeeded, want error")
	}
	for _, filter := range []TrendFixedVariableFilter{
		{},
		{From: "bad", To: "2026-07-31"},
		{From: "2026-08-01", To: "2026-07-31"},
	} {
		if _, err := ReadTrendFixedVariable(ctx, db, filter); err == nil {
			t.Errorf("ReadTrendFixedVariable(%+v) succeeded, want error", filter)
		}
	}
}

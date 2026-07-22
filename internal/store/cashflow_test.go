package store

import (
	"context"
	"testing"
)

func TestReadCashflowAggregatesPostedNonExcludedRows(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")

	// Included, with both inclusive bounds represented.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-01", 250000, "Employer Example", int64(1), "posted", 0, "income")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-15", 5000, "Refund Example", int64(7), "posted", 0, "refund")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-31", -180000, "Bills Example", int64(16), "posted", 0, "outflow")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-20", 0, "Zero Example", nil, "posted", 0, "zero")
	// Excluded, pending, and outside-period rows do not participate.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-10", 500000, "Transfer In", int64(2), "posted", 1, "transfer-in")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-10", -500000, "Transfer Out", int64(3), "posted", 1, "transfer-out")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-10", -3000, "Pending Example", nil, "pending", 0, "pending")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-30", 100000, "Outside Example", nil, "posted", 0, "outside")

	summary, err := ReadCashflow(ctx, db, CashflowFilter{
		From: "2026-07-01", To: "2026-07-31",
	})
	if err != nil {
		t.Fatalf("ReadCashflow() error: %v", err)
	}
	want := CashflowSummary{
		Count:        4,
		InflowCents:  255000,
		OutflowCents: 180000,
		NetCents:     75000,
	}
	if summary != want {
		t.Errorf("ReadCashflow() = %+v, want %+v", summary, want)
	}
}

func TestReadCashflowAccountFilterIsLiteral(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	percentID := insertAccountFull(t, db, entityID, "Percent_Account", "checking", "acct-1")
	otherID := insertAccountFull(t, db, entityID, "Other Account", "checking", "acct-2")
	insertSpendTransaction(t, db, percentID, entityID,
		"2026-07-10", 10000, "Income One", nil, "posted", 0, "percent")
	insertSpendTransaction(t, db, otherID, entityID,
		"2026-07-10", 20000, "Income Two", nil, "posted", 0, "other")

	summary, err := ReadCashflow(ctx, db, CashflowFilter{
		From: "2026-07-01", To: "2026-07-31", Account: "_",
	})
	if err != nil {
		t.Fatalf("ReadCashflow() error: %v", err)
	}
	if summary.Count != 1 || summary.InflowCents != 10000 {
		t.Errorf("summary = %+v, want only literal-underscore account", summary)
	}
}

func TestReadCashflowEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	summary, err := ReadCashflow(ctx, db, CashflowFilter{
		From: "2026-07-01", To: "2026-07-31",
	})
	if err != nil {
		t.Fatalf("ReadCashflow() error: %v", err)
	}
	if summary != (CashflowSummary{}) {
		t.Errorf("empty summary = %+v", summary)
	}

	filters := []CashflowFilter{
		{},
		{From: "2026-07-01"},
		{From: "bad", To: "2026-07-31"},
		{From: "2026-07-31", To: "2026-07-01"},
	}
	for _, filter := range filters {
		if _, err := ReadCashflow(ctx, db, filter); err == nil {
			t.Errorf("ReadCashflow(%+v) succeeded, want an error", filter)
		}
	}
	if _, err := ReadCashflow(ctx, nil, CashflowFilter{
		From: "2026-07-01", To: "2026-07-31",
	}); err == nil {
		t.Error("ReadCashflow(nil db) succeeded, want an error")
	}
}

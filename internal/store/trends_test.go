package store

import (
	"context"
	"testing"
	"time"
)

func TestResolveTrendMoMPeriod(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	period, err := ResolveTrendMoMPeriod("", now)
	if err != nil {
		t.Fatalf("ResolveTrendMoMPeriod(default) error: %v", err)
	}
	want := (TrendMoMPeriod{
		ThisFrom: "2026-07-01",
		ThisTo:   "2026-07-31",
		PrevFrom: "2026-06-01",
		PrevTo:   "2026-06-30",
	})
	if period != want {
		t.Errorf("default period = %+v, want %+v", period, want)
	}

	period, err = ResolveTrendMoMPeriod("2024-03", now)
	if err != nil {
		t.Fatalf("ResolveTrendMoMPeriod(leap) error: %v", err)
	}
	want = TrendMoMPeriod{
		ThisFrom: "2024-03-01",
		ThisTo:   "2024-03-31",
		PrevFrom: "2024-02-01",
		PrevTo:   "2024-02-29",
	}
	if period != want {
		t.Errorf("leap period = %+v, want %+v", period, want)
	}
	for _, value := range []string{"2026-13", "2026-7", "July"} {
		if _, err := ResolveTrendMoMPeriod(value, now); err == nil {
			t.Errorf("ResolveTrendMoMPeriod(%q) succeeded, want error", value)
		}
	}
}

func TestReadTrendMoMComputesCategoryDeltasAndExcludesNonSpend(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")

	rootResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('Same Name', 'expense')
	`)
	if err != nil {
		t.Fatalf("insert root category: %v", err)
	}
	rootID, err := rootResult.LastInsertId()
	if err != nil {
		t.Fatalf("root category id: %v", err)
	}
	parentResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('Parent Example', 'expense')
	`)
	if err != nil {
		t.Fatalf("insert parent category: %v", err)
	}
	parentID, err := parentResult.LastInsertId()
	if err != nil {
		t.Fatalf("parent category id: %v", err)
	}
	childResult, err := db.Exec(`
		INSERT INTO categories (name, parent_id, kind) VALUES ('Same Name', ?, 'expense')
	`, parentID)
	if err != nil {
		t.Fatalf("insert child category: %v", err)
	}
	childID, err := childResult.LastInsertId()
	if err != nil {
		t.Fatalf("child category id: %v", err)
	}

	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-10", -3000, "Previous Food", int64(7), "posted", 0, "prev-food")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-10", -4000, "Current Food", int64(7), "posted", 0, "this-food")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-11", -5000, "Previous General", int64(8), "posted", 0, "prev-general")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-11", -1000, "Current General", int64(8), "posted", 0, "this-general")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-12", -1500, "Previous Transit", int64(14), "posted", 0, "prev-transit")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-12", -2000, "Current Unknown", nil, "posted", 0, "this-unknown")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-06-13", -100, "Previous Same", rootID, "posted", 0, "prev-same")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-13", -100, "Current Same", childID, "posted", 0, "this-same")

	// These rows must not affect any MoM total or category.
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-14", -900000, "Excluded Transfer", int64(2), "posted", 1, "excluded")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-15", -800000, "Pending Shop", int64(7), "pending", 0, "pending")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-07-16", 700000, "Income Example", int64(1), "posted", 0, "income")
	insertSpendTransaction(t, db, accountID, entityID,
		"2026-05-31", -600000, "Outside Shop", int64(7), "posted", 0, "outside")

	filter := TrendMoMFilter{
		ThisFrom: "2026-07-01",
		ThisTo:   "2026-07-31",
		PrevFrom: "2026-06-01",
		PrevTo:   "2026-06-30",
	}
	report, err := ReadTrendMoM(ctx, db, filter, 0)
	if err != nil {
		t.Fatalf("ReadTrendMoM() error: %v", err)
	}
	if report.SpendThisCents != 7100 || report.SpendPrevCents != 9600 ||
		report.DeltaCents != -2500 || report.CategoryTotal != 6 {
		t.Errorf("summary = this %d / prev %d / delta %d / categories %d, want 7100/9600/-2500/6",
			report.SpendThisCents, report.SpendPrevCents, report.DeltaCents, report.CategoryTotal)
	}
	want := []TrendMoMCategory{
		{Name: "General Merchandise", SpendThisCents: 1000, SpendPrevCents: 5000, DeltaCents: -4000},
		{Name: "Uncategorized", SpendThisCents: 2000, SpendPrevCents: 0, DeltaCents: 2000},
		{Name: "Transportation", SpendThisCents: 0, SpendPrevCents: 1500, DeltaCents: -1500},
		{Name: "Food and Drink", SpendThisCents: 4000, SpendPrevCents: 3000, DeltaCents: 1000},
		{Name: "Same Name", SpendThisCents: 0, SpendPrevCents: 100, DeltaCents: -100},
		{Name: "Same Name", SpendThisCents: 100, SpendPrevCents: 0, DeltaCents: 100},
	}
	if len(report.Categories) != len(want) {
		t.Fatalf("categories = %d, want %d: %+v", len(report.Categories), len(want), report.Categories)
	}
	for index := range want {
		if report.Categories[index] != want[index] {
			t.Errorf("categories[%d] = %+v, want %+v", index, report.Categories[index], want[index])
		}
	}

	limited, err := ReadTrendMoM(ctx, db, filter, 3)
	if err != nil {
		t.Fatalf("ReadTrendMoM(limit) error: %v", err)
	}
	if len(limited.Categories) != 3 || limited.CategoryTotal != 6 ||
		limited.SpendThisCents != 7100 || limited.SpendPrevCents != 9600 {
		t.Errorf("limited report = %+v", limited)
	}
}

func TestReadTrendMoMAccountFilterIsLiteral(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	underscoreID := insertAccountFull(t, db, entityID, "Percent_Account", "checking", "acct-1")
	otherID := insertAccountFull(t, db, entityID, "PercentXAccount", "checking", "acct-2")
	insertSpendTransaction(t, db, underscoreID, entityID,
		"2026-07-10", -100, "Literal Match", nil, "posted", 0, "literal")
	insertSpendTransaction(t, db, otherID, entityID,
		"2026-07-10", -200, "Wildcard Match", nil, "posted", 0, "wildcard")

	report, err := ReadTrendMoM(ctx, db, TrendMoMFilter{
		ThisFrom: "2026-07-01",
		ThisTo:   "2026-07-31",
		PrevFrom: "2026-06-01",
		PrevTo:   "2026-06-30",
		Account:  "_",
	}, 20)
	if err != nil {
		t.Fatalf("ReadTrendMoM() error: %v", err)
	}
	if report.SpendThisCents != 100 || report.CategoryTotal != 1 {
		t.Errorf("filtered report = %+v, want only literal underscore account", report)
	}
}

func TestReadTrendMoMEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	valid := TrendMoMFilter{
		ThisFrom: "2026-07-01",
		ThisTo:   "2026-07-31",
		PrevFrom: "2026-06-01",
		PrevTo:   "2026-06-30",
	}
	report, err := ReadTrendMoM(ctx, db, valid, 20)
	if err != nil {
		t.Fatalf("ReadTrendMoM() error: %v", err)
	}
	if report.SpendThisCents != 0 || report.SpendPrevCents != 0 ||
		report.DeltaCents != 0 || report.CategoryTotal != 0 || len(report.Categories) != 0 {
		t.Errorf("empty report = %+v", report)
	}

	filters := []TrendMoMFilter{
		{},
		{ThisFrom: "bad", ThisTo: "2026-07-31", PrevFrom: "2026-06-01", PrevTo: "2026-06-30"},
		{ThisFrom: "2026-07-01", ThisTo: "2026-07-31", PrevFrom: "2026-08-01", PrevTo: "2026-08-31"},
	}
	for _, filter := range filters {
		if _, err := ReadTrendMoM(ctx, db, filter, 20); err == nil {
			t.Errorf("ReadTrendMoM(%+v) succeeded, want error", filter)
		}
	}
	if _, err := ReadTrendMoM(ctx, nil, valid, 20); err == nil {
		t.Error("ReadTrendMoM(nil db) succeeded, want error")
	}
}

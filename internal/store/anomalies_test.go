package store

import (
	"context"
	"database/sql"
	"math"
	"testing"
)

func TestReadAnomaliesFlagsOnlyTrueSpikes(t *testing.T) {
	t.Run("default previous month filters eligibility ordering and new account", func(t *testing.T) {
		ctx := context.Background()
		db := openTestDB(t)
		entityID := insertEntity(t, db, "personal", "Personal")
		oldAccount := insertAnomalyAccount(t, db, entityID, "History Checking", "anomaly-old")
		newAccount := insertAnomalyAccount(t, db, entityID, "New Card", "anomaly-new")
		foodID := insertAnomalyCategory(t, db, "Food Spike", nil)
		newCategoryID := insertAnomalyCategory(t, db, "Shared History", nil)
		alphaID := insertAnomalyCategory(t, db, "Alpha Spike", nil)
		betaID := insertAnomalyCategory(t, db, "Beta Spike", nil)
		travelID := insertAnomalyCategory(t, db, "Travel Spike", nil)
		smallID := insertAnomalyCategory(t, db, "Small Difference", nil)
		notDoubleID := insertAnomalyCategory(t, db, "Not Double", nil)
		sparseID := insertAnomalyCategory(t, db, "Sparse Baseline", nil)
		zeroID := insertAnomalyCategory(t, db, "Zero Baseline", nil)
		parentOneID := insertAnomalyCategory(t, db, "Parent One", nil)
		parentTwoID := insertAnomalyCategory(t, db, "Parent Two", nil)
		sharedOneID := insertAnomalyCategory(t, db, "Same Name Spike", &parentOneID)
		sharedTwoID := insertAnomalyCategory(t, db, "Same Name Spike", &parentTwoID)
		sequence := 0
		add := func(accountID int64, date string, categoryID *int64, spend int64) {
			t.Helper()
			sequence++
			insertAnomalyTransaction(t, db, accountID, entityID, date, -spend, categoryID,
				"posted", false, false, sequence)
		}
		baseline := []string{"2026-01-15", "2026-02-15", "2026-03-15"}
		for _, date := range baseline {
			add(oldAccount, date, &foodID, 10000)
			add(oldAccount, date, &newCategoryID, 5000)
			add(oldAccount, date, &alphaID, 4000)
			add(oldAccount, date, &betaID, 4000)
			add(oldAccount, date, nil, 3000)
			add(oldAccount, date, &smallID, 1000)
			add(oldAccount, date, &notDoubleID, 10000)
			add(oldAccount, date, &sharedOneID, 3000)
			add(oldAccount, date, &sharedTwoID, 3000)
		}
		add(oldAccount, "2026-01-16", &travelID, 4000)
		add(oldAccount, "2026-02-16", &travelID, 4000)
		add(oldAccount, "2026-01-17", &sparseID, 10000)

		add(oldAccount, "2026-04-15", &foodID, 31000)
		add(newAccount, "2026-04-15", &newCategoryID, 16000)
		add(oldAccount, "2026-04-15", &alphaID, 14000)
		add(oldAccount, "2026-04-15", &betaID, 14000)
		add(oldAccount, "2026-04-15", nil, 12000)
		add(oldAccount, "2026-04-15", &travelID, 9000)
		add(oldAccount, "2026-04-15", &smallID, 2500)
		add(oldAccount, "2026-04-15", &notDoubleID, 19000)
		add(oldAccount, "2026-04-15", &sparseID, 50000)
		add(oldAccount, "2026-04-15", &zeroID, 50000)
		add(oldAccount, "2026-04-15", &sharedOneID, 11000)
		add(oldAccount, "2026-04-15", &sharedTwoID, 11000)

		// Non-spend rows must not inflate an otherwise deterministic food spike.
		sequence++
		insertAnomalyTransaction(t, db, oldAccount, entityID, "2026-04-16", -100000,
			&foodID, "pending", false, false, sequence)
		sequence++
		insertAnomalyTransaction(t, db, oldAccount, entityID, "2026-04-17", -100000,
			&foodID, "posted", true, false, sequence)
		sequence++
		insertAnomalyTransaction(t, db, oldAccount, entityID, "2026-04-18", -100000,
			&foodID, "posted", false, true, sequence)
		sequence++
		insertAnomalyTransaction(t, db, oldAccount, entityID, "2026-04-19", 100000,
			&foodID, "posted", false, false, sequence)

		report, err := ReadAnomalies(ctx, db, "2026-05-20", "")
		if err != nil {
			t.Fatalf("ReadAnomalies() error: %v", err)
		}
		if report.Period != "2026-04" || report.SkippedOverflow != 0 {
			t.Errorf("summary = period %q skipped %d, want 2026-04 / 0",
				report.Period, report.SkippedOverflow)
		}
		wantNames := []string{
			"Food Spike", "Shared History", "Alpha Spike", "Beta Spike",
			"Uncategorized", "Same Name Spike", "Same Name Spike", "Travel Spike",
		}
		if len(report.Items) != len(wantNames) {
			t.Fatalf("items = %+v, want names %v", report.Items, wantNames)
		}
		for index, want := range wantNames {
			if report.Items[index].Category != want {
				t.Errorf("item %d category = %q, want %q", index, report.Items[index].Category, want)
			}
		}
		food := report.Items[0]
		if food.SpendCents != 31000 || food.BaselineCents != 10000 || food.DeltaCents != 21000 {
			t.Errorf("food spike = %+v, want spend 31000 baseline 10000 delta 21000", food)
		}
		travel := report.Items[len(report.Items)-1]
		if travel.BaselineCents != 2666 || travel.DeltaCents != 6334 {
			t.Errorf("travel mean = %+v, want baseline 2666 delta 6334", travel)
		}
		if report.Items[5].CategoryID == nil || report.Items[6].CategoryID == nil ||
			*report.Items[5].CategoryID != sharedOneID || *report.Items[6].CategoryID != sharedTwoID ||
			sharedOneID >= sharedTwoID {
			t.Errorf("same-name category-id order = %+v / %+v", report.Items[5], report.Items[6])
		}
		for _, name := range []string{"Small Difference", "Not Double", "Sparse Baseline", "Zero Baseline"} {
			if anomalyByName(report.Items, name) != nil {
				t.Errorf("false spike %q was flagged: %+v", name, report.Items)
			}
		}
	})

	t.Run("explicit current month compares equal MTD slices", func(t *testing.T) {
		ctx := context.Background()
		db := openTestDB(t)
		entityID := insertEntity(t, db, "personal", "Personal")
		accountID := insertAnomalyAccount(t, db, entityID, "MTD Checking", "anomaly-mtd")
		categoryID := insertAnomalyCategory(t, db, "MTD Spike", nil)
		sequence := 0
		add := func(date string, spend int64) {
			sequence++
			insertAnomalyTransaction(t, db, accountID, entityID, date, -spend, &categoryID,
				"posted", false, false, sequence)
		}
		add("2025-12-05", 1000)
		add("2025-12-20", 100000)
		add("2026-01-10", 2000)
		add("2026-01-11", 100000)
		add("2026-02-10", 3000)
		add("2026-02-28", 100000)
		add("2026-03-10", 8000)
		add("2026-03-11", 100000)

		report, err := ReadAnomalies(ctx, db, "2026-03-10", "2026-03")
		if err != nil {
			t.Fatalf("ReadAnomalies(MTD) error: %v", err)
		}
		if report.Period != "2026-03" || len(report.Items) != 1 {
			t.Fatalf("MTD report = %+v", report)
		}
		item := report.Items[0]
		if item.SpendCents != 8000 || item.BaselineCents != 2000 || item.DeltaCents != 6000 {
			t.Errorf("MTD anomaly = %+v, want spend 8000 baseline 2000 delta 6000", item)
		}
	})

	t.Run("future period and malformed inputs are rejected", func(t *testing.T) {
		db := openTestDB(t)
		for _, test := range []struct {
			asOf   string
			period string
		}{
			{"2026-05-20", "2026-06"},
			{"2026-05-20", "2026-13"},
			{"bad", "2026-05"},
		} {
			if _, err := ReadAnomalies(context.Background(), db, test.asOf, test.period); err == nil {
				t.Errorf("ReadAnomalies(%q, %q) succeeded", test.asOf, test.period)
			}
		}
		if _, err := ReadAnomalies(context.Background(), nil, "2026-05-20", "2026-04"); err == nil {
			t.Error("ReadAnomalies(nil db) succeeded")
		}
	})

	t.Run("overflow skips category and remains observable when rows are empty", func(t *testing.T) {
		ctx := context.Background()
		db := openTestDB(t)
		entityID := insertEntity(t, db, "personal", "Personal")
		accountID := insertAnomalyAccount(t, db, entityID, "Boundary Checking", "anomaly-boundary")
		categoryID := insertAnomalyCategory(t, db, "Boundary Spike", nil)
		baselineSpend := int64(math.MaxInt64/2 + 100)
		for index, date := range []string{"2026-01-15", "2026-02-15", "2026-03-15"} {
			insertAnomalyTransaction(t, db, accountID, entityID, date, -baselineSpend,
				&categoryID, "posted", false, false, index+1)
		}
		insertAnomalyTransaction(t, db, accountID, entityID, "2026-04-15", -10000,
			&categoryID, "posted", false, false, 4)

		report, err := ReadAnomalies(ctx, db, "2026-05-20", "2026-04")
		if err != nil {
			t.Fatalf("ReadAnomalies(overflow) error: %v", err)
		}
		if len(report.Items) != 0 || report.SkippedOverflow != 1 {
			t.Errorf("overflow report = %+v, want empty with skipped_overflow 1", report)
		}
	})
}

func insertAnomalyAccount(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	name, providerID string,
) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, provider, provider_account_id
		) VALUES (?, 'checking', ?, 'plaid', ?)
	`, entityID, name, providerID)
	if err != nil {
		t.Fatalf("insert anomaly account: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("anomaly account id: %v", err)
	}
	return id
}

func insertAnomalyCategory(
	t *testing.T,
	db *sql.DB,
	name string,
	parentID *int64,
) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO categories (name, parent_id, kind) VALUES (?, ?, 'expense')
	`, name, parentID)
	if err != nil {
		t.Fatalf("insert anomaly category %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("anomaly category id: %v", err)
	}
	return id
}

func insertAnomalyTransaction(
	t *testing.T,
	db *sql.DB,
	accountID, entityID int64,
	date string,
	amount int64,
	categoryID *int64,
	status string,
	excluded, transfer bool,
	sequence int,
) {
	t.Helper()
	excludedValue := 0
	if excluded {
		excludedValue = 1
	}
	transferValue := 0
	if transfer {
		transferValue = 1
	}
	if _, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, category_id, status,
			excluded, is_transfer, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, printf('anomaly-%d', ?))
	`, accountID, entityID, date, amount, categoryID, status,
		excludedValue, transferValue, sequence); err != nil {
		t.Fatalf("insert anomaly transaction %d: %v", sequence, err)
	}
}

func anomalyByName(items []AnomalyItem, name string) *AnomalyItem {
	for index := range items {
		if items[index].Category == name {
			return &items[index]
		}
	}
	return nil
}

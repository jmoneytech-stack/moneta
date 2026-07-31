package store

import (
	"context"
	"database/sql"
	"math"
	"testing"
)

func TestReadBillsMergesRecurringAndCards(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	setBillsDetector(t, db, DetectorState{Status: "ok"})
	insertBillsRecurring(t, db, entityID, "Hosting Example", "subscription", "monthly", -2500, "2026-02-10", true, 10)
	cardID := insertBillsAccount(t, db, entityID, "Month End Card", "credit_card", true)
	insertBillsCardTerms(t, db, cardID, int64Pointer(3500), int64Pointer(31), stringPointer("2026-01-31"))

	report, err := ReadBills(ctx, db, "2026-02-05", 30)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if report.AsOf != "2026-02-05" || report.Through != "2026-03-07" || report.Days != 30 {
		t.Errorf("window = %s through %s (%d), want 2026-02-05 through 2026-03-07 (30)",
			report.AsOf, report.Through, report.Days)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items = %+v, want recurring and card", report.Items)
	}
	assertBillItem(t, report.Items[0], BillItem{
		Date: "2026-02-10", Name: "Hosting Example", AmountCents: int64Pointer(2500),
		Source: "recurring", Kind: "subscription", DateSource: "detected_schedule", DueStatus: "upcoming",
	})
	assertBillItem(t, report.Items[1], BillItem{
		Date: "2026-02-28", Name: "Month End Card", AmountCents: int64Pointer(3500),
		Source: "card_due", Kind: "bill", DateSource: "projected_from_past_provider_date", DueStatus: "upcoming",
	})
}

func TestReadBillsIncludesDueOnAsOfAndInGrace(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	setBillsDetector(t, db, DetectorState{Status: "ok"})
	insertBillsRecurring(t, db, entityID, "Due Subscription", "subscription", "monthly", -1200, "2026-07-01", true, 1)
	insertBillsRecurring(t, db, entityID, "Grace Bill", "bill", "monthly", -900, "2026-06-29", true, 29)
	insertBillsRecurring(t, db, entityID, "Expired Bill", "bill", "monthly", -800, "2026-06-27", true, 27)
	cardGraceID := insertBillsAccount(t, db, entityID, "Grace Card", "credit_card", true)
	insertBillsCardTerms(t, db, cardGraceID, nil, int64Pointer(29), stringPointer("2026-06-29"))
	cardDueID := insertBillsAccount(t, db, entityID, "Due Card", "credit_card", true)
	insertBillsCardTerms(t, db, cardDueID, int64Pointer(4500), int64Pointer(1), stringPointer("2026-07-01"))

	report, err := ReadBills(ctx, db, "2026-07-01", 30)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if len(report.Items) != 4 {
		t.Fatalf("items = %+v, want two due and two in grace", report.Items)
	}
	byName := billsByName(report.Items)
	for name, status := range map[string]string{
		"Due Subscription": "due", "Grace Bill": "in_grace",
		"Grace Card": "in_grace", "Due Card": "due",
	} {
		if byName[name].DueStatus != status {
			t.Errorf("%s due_status = %q, want %q", name, byName[name].DueStatus, status)
		}
	}
	if amount := byName["Due Subscription"].AmountCents; amount == nil || *amount != 1200 {
		t.Errorf("recurring obligation amount = %v, want non-negative 1200", amount)
	}
	if byName["Due Card"].Kind != "bill" || byName["Due Card"].DateSource != "provider_reported" {
		t.Errorf("due card = %+v, want bill/provider_reported", byName["Due Card"])
	}
	if byName["Grace Card"].AmountCents != nil {
		t.Errorf("missing card minimum rendered as %v, want nil", byName["Grace Card"].AmountCents)
	}
	if _, found := byName["Expired Bill"]; found {
		t.Error("expired recurring bill remained visible after grace")
	}
}

func TestReadBillsCardInGraceInsideHorizon(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	inGraceID := insertBillsAccount(t, db, entityID, "Just Past Card", "credit_card", true)
	insertBillsCardTerms(t, db, inGraceID, int64Pointer(1000), int64Pointer(1), stringPointer("2026-07-01"))
	projectedInsideID := insertBillsAccount(t, db, entityID, "Projected Inside Card", "credit_card", true)
	insertBillsCardTerms(t, db, projectedInsideID, int64Pointer(2000), int64Pointer(30), stringPointer("2026-05-31"))
	projectedOutsideID := insertBillsAccount(t, db, entityID, "Projected Outside Card", "credit_card", true)
	insertBillsCardTerms(t, db, projectedOutsideID, int64Pointer(3000), int64Pointer(20), stringPointer("2026-05-20"))
	providerOutsideID := insertBillsAccount(t, db, entityID, "Provider Outside Card", "credit_card", true)
	insertBillsCardTerms(t, db, providerOutsideID, int64Pointer(4000), int64Pointer(20), stringPointer("2026-08-20"))

	report, err := ReadBills(ctx, db, "2026-07-03", 10)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if len(report.Items) != 2 {
		t.Fatalf("items = %+v, want in-grace and projected-inside cards", report.Items)
	}
	byName := billsByName(report.Items)
	if item := byName["Just Past Card"]; item.Date != "2026-07-01" ||
		item.DateSource != "provider_reported" || item.DueStatus != "in_grace" {
		t.Errorf("just-past card = %+v", item)
	}
	if item := byName["Projected Inside Card"]; item.Date != "2026-06-30" ||
		item.DateSource != "projected_from_past_provider_date" || item.DueStatus != "in_grace" {
		t.Errorf("projected-inside card = %+v", item)
	}
	for _, name := range []string{"Projected Outside Card", "Provider Outside Card"} {
		if _, found := byName[name]; found {
			t.Errorf("%s escaped horizon filtering", name)
		}
	}
}

func TestReadBillsActiveCardsOnly(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	activeID := insertBillsAccount(t, db, entityID, "Active Card", "credit_card", true)
	insertBillsCardTerms(t, db, activeID, nil, int64Pointer(15), nil)
	inactiveID := insertBillsAccount(t, db, entityID, "Inactive Card", "credit_card", false)
	insertBillsCardTerms(t, db, inactiveID, nil, int64Pointer(15), nil)
	checkingID := insertBillsAccount(t, db, entityID, "Checking Example", "checking", true)
	insertBillsCardTerms(t, db, checkingID, nil, int64Pointer(15), nil)

	report, err := ReadBills(ctx, db, "2026-07-01", 30)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if len(report.Items) != 1 || report.Items[0].Name != "Active Card" ||
		report.Items[0].Date != "2026-07-15" || report.Items[0].DateSource != "day_of_month_estimate" {
		t.Errorf("active card projection = %+v", report.Items)
	}
}

func TestReadBillsOmitsDetectedWhenNeverRunOrErrorKeepsCards(t *testing.T) {
	for _, status := range []string{"never_run", "error", "partial"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			entityID := insertEntity(t, db, "personal", "Personal")
			if status != "never_run" {
				setBillsDetector(t, db, DetectorState{Status: status})
			}
			insertBillsRecurring(t, db, entityID, "Detected Bill", "bill", "monthly", -1500, "2026-07-20", true, 20)
			cardID := insertBillsAccount(t, db, entityID, "Card Due", "credit_card", true)
			insertBillsCardTerms(t, db, cardID, int64Pointer(2500), int64Pointer(25), stringPointer("2026-07-25"))

			report, err := ReadBills(ctx, db, "2026-07-01", 30)
			if err != nil {
				t.Fatalf("ReadBills() error: %v", err)
			}
			wantCount := 1
			if status == "partial" {
				wantCount = 2
			}
			if len(report.Items) != wantCount {
				t.Fatalf("status %s items = %+v, want %d", status, report.Items, wantCount)
			}
			if _, found := billsByName(report.Items)["Card Due"]; !found {
				t.Errorf("status %s omitted card due", status)
			}
			_, detected := billsByName(report.Items)["Detected Bill"]
			if detected != (status == "partial") {
				t.Errorf("status %s detected present = %v", status, detected)
			}
		})
	}
}

func TestReadBillsPartialProjectsRecurringBeyondStoredGraceWithoutMutatingRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	setBillsDetector(t, db, DetectorState{Status: "partial"})
	rowID := insertBillsRecurring(t, db, entityID, "Persistent Bill", "bill", "monthly", -1900, "2026-06-01", true, 1)
	if _, err := db.Exec("UPDATE recurring_items SET miss_count = 7 WHERE id = ?", rowID); err != nil {
		t.Fatalf("seed miss count: %v", err)
	}

	report, err := ReadBills(ctx, db, "2026-07-10", 30)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if len(report.Items) != 1 || report.Items[0].Date != "2026-08-01" ||
		report.Items[0].DueStatus != "upcoming" {
		t.Fatalf("partial projected item = %+v, want 2026-08-01 upcoming", report.Items)
	}
	var storedDate string
	var missCount, active int
	if err := db.QueryRow(`
		SELECT next_expected_date, miss_count, is_active FROM recurring_items WHERE id = ?
	`, rowID).Scan(&storedDate, &missCount, &active); err != nil {
		t.Fatalf("read stored lifecycle: %v", err)
	}
	if storedDate != "2026-06-01" || missCount != 7 || active != 1 {
		t.Errorf("stored lifecycle mutated to date=%s misses=%d active=%d", storedDate, missCount, active)
	}
}

func TestReadBillsDetectorSummaryFourFields(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	want := DetectorState{
		Status: "partial", LastRunAt: "2026-07-15T12:00:00.000Z",
		LastSuccessAt: "2026-07-01T12:00:00.000Z", LastSkippedOverflow: 4,
	}
	setBillsDetector(t, db, want)
	report, err := ReadBills(ctx, db, "2026-07-01", 30)
	if err != nil {
		t.Fatalf("ReadBills() error: %v", err)
	}
	if report.Detector != want {
		t.Errorf("detector = %+v, want %+v", report.Detector, want)
	}
}

func TestReadBillsValidatesWindowAndCheckedMagnitude(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	for _, test := range []struct {
		name string
		db   *sql.DB
		asOf string
		days int
	}{
		{"nil database", nil, "2026-07-01", 30},
		{"bad as-of", db, "2026-02-30", 30},
		{"zero days", db, "2026-07-01", 0},
		{"too many days", db, "2026-07-01", 367},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadBills(ctx, test.db, test.asOf, test.days); err == nil {
				t.Fatal("ReadBills() accepted invalid input")
			}
		})
	}

	entityID := insertEntity(t, db, "personal", "Personal")
	setBillsDetector(t, db, DetectorState{Status: "ok"})
	insertBillsRecurring(t, db, entityID, "Magnitude Overflow", "bill", "monthly", math.MinInt64, "2026-07-15", true, 15)
	if _, err := ReadBills(ctx, db, "2026-07-01", 30); err == nil {
		t.Fatal("ReadBills() accepted abs(math.MinInt64)")
	}
}

func insertBillsRecurring(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	name, kind, cadence string,
	expected int64,
	nextDate string,
	active bool,
	anchor int,
) int64 {
	t.Helper()
	activeValue := 0
	if active {
		activeValue = 1
	}
	result, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, schedule_anchor_day
		) VALUES (?, ?, ?, ?, ?, ?, 'detected', ?, ?, -1, ?)
	`, entityID, name, kind, cadence, expected, nextDate, activeValue, name, anchor)
	if err != nil {
		t.Fatalf("insert recurring bill %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("recurring bill id: %v", err)
	}
	return id
}

func insertBillsAccount(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	name, accountType string,
	active bool,
) int64 {
	t.Helper()
	activeValue := 0
	if active {
		activeValue = 1
	}
	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, provider, provider_account_id, is_active
		) VALUES (?, ?, ?, 'plaid', ?, ?)
	`, entityID, accountType, name, "bills-"+name, activeValue)
	if err != nil {
		t.Fatalf("insert bills account %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("bills account id: %v", err)
	}
	return id
}

func insertBillsCardTerms(
	t *testing.T,
	db *sql.DB,
	accountID int64,
	minimum, dueDay *int64,
	fullDate *string,
) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO credit_terms (
			account_id, min_payment_cents, due_day, next_payment_due_date
		) VALUES (?, ?, ?, ?)
	`, accountID, minimum, dueDay, fullDate); err != nil {
		t.Fatalf("insert card terms: %v", err)
	}
}

func setBillsDetector(t *testing.T, db *sql.DB, state DetectorState) {
	t.Helper()
	if err := UpsertDetectorState(context.Background(), db, state); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
}

func assertBillItem(t *testing.T, got, want BillItem) {
	t.Helper()
	if got.Date != want.Date || got.Name != want.Name || got.Source != want.Source ||
		got.Kind != want.Kind || got.DateSource != want.DateSource || got.DueStatus != want.DueStatus {
		t.Errorf("bill = %+v, want %+v", got, want)
		return
	}
	if got.AmountCents == nil || want.AmountCents == nil {
		if got.AmountCents != nil || want.AmountCents != nil {
			t.Errorf("bill amount = %v, want %v", got.AmountCents, want.AmountCents)
		}
		return
	}
	if *got.AmountCents != *want.AmountCents {
		t.Errorf("bill amount = %d, want %d", *got.AmountCents, *want.AmountCents)
	}
}

func billsByName(items []BillItem) map[string]BillItem {
	result := make(map[string]BillItem, len(items))
	for _, item := range items {
		result[item.Name] = item
	}
	return result
}

func stringPointer(value string) *string {
	return &value
}

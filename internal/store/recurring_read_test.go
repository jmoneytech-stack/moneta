package store

import (
	"context"
	"database/sql"
	"math"
	"testing"
)

func TestReadRecurringListsActiveAndInactive(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if err := UpsertDetectorState(ctx, db, DetectorState{
		Status: "ok", LastRunAt: "2026-08-15T12:00:00.000Z",
		LastSuccessAt: "2026-08-15T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Weekly Streambox", kind: "subscription",
		cadence: "weekly", expected: -1200, nextDate: "2026-08-20",
		source: "detected", active: true, detectKey: "weekly streambox", amountSign: -1,
		lastMatchedCents: int64Pointer(-1200), anchor: 20,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Rent Example", kind: "bill",
		cadence: "monthly", expected: -100000, nextDate: "2026-09-01",
		source: "detected", active: true, detectKey: "rent example", amountSign: -1,
		lastMatchedCents: int64Pointer(-100000), anchor: 1,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Payroll Example", kind: "income",
		cadence: "monthly", expected: 200000, nextDate: "2026-08-25",
		source: "detected", active: true, detectKey: "payroll example", amountSign: 1,
		lastMatchedCents: int64Pointer(200000), anchor: 25,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Inactive Example", kind: "subscription",
		cadence: "monthly", expected: -1500, nextDate: "2026-07-01",
		source: "detected", active: false, detectKey: "inactive example", amountSign: -1,
		lastMatchedCents: int64Pointer(-1500), anchor: 1,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Manual Bill Example", kind: "bill",
		cadence: "monthly", expected: -50000, nextDate: "2026-10-01",
		source: "manual", active: true,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Yearly Detected Example", kind: "bill",
		cadence: "yearly", expected: -240000, nextDate: "2026-08-30",
		source: "detected", active: true, detectKey: "yearly detected", amountSign: -1,
		lastMatchedCents: int64Pointer(-240000), anchor: 30,
	})

	report, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15"})
	if err != nil {
		t.Fatalf("ReadRecurring() error: %v", err)
	}
	if len(report.Items) != 6 {
		t.Fatalf("ReadRecurring() items = %d, want 6", len(report.Items))
	}
	if report.ActiveDetected.Subscription != 1 || report.ActiveDetected.Bill != 2 ||
		report.ActiveDetected.Income != 1 {
		t.Errorf("active detected counts = %+v, want subscription 1 / bill 2 / income 1",
			report.ActiveDetected)
	}
	if report.MonthlyEquivalentCents != -105200 || report.MonthlyEquivalentUnconverted != 1 {
		t.Errorf("monthly equivalent = %d / unconverted %d, want -105200 / 1",
			report.MonthlyEquivalentCents, report.MonthlyEquivalentUnconverted)
	}
	for index := 0; index < len(report.Items)-1; index++ {
		if !report.Items[index].Active {
			t.Errorf("item %d is inactive before final inactive row: %+v", index, report.Items)
		}
	}
	if report.Items[len(report.Items)-1].Name != "Inactive Example" ||
		report.Items[len(report.Items)-1].Active {
		t.Errorf("last item = %+v, want inactive example", report.Items[len(report.Items)-1])
	}

	bills, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15", Kind: "bill"})
	if err != nil {
		t.Fatalf("ReadRecurring(kind=bill) error: %v", err)
	}
	if len(bills.Items) != 3 {
		t.Errorf("bill-filtered items = %d, want 3", len(bills.Items))
	}
	if bills.ActiveDetected != report.ActiveDetected ||
		bills.MonthlyEquivalentCents != report.MonthlyEquivalentCents {
		t.Errorf("kind filter changed global summary: %+v vs %+v", bills, report)
	}
}

func TestReadRecurringOmitsDetectedWhenNeverRunOrError(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Detected Example", kind: "subscription",
		cadence: "monthly", expected: -1500, nextDate: "2026-09-01",
		source: "detected", active: true, detectKey: "detected example", amountSign: -1,
		lastMatchedCents: int64Pointer(-1500), anchor: 1,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Manual Example", kind: "subscription",
		cadence: "monthly", expected: -2000, nextDate: "2026-09-02",
		source: "manual", active: true,
	})

	for _, test := range []struct {
		status       string
		wantDetected bool
	}{
		{status: "never_run", wantDetected: false},
		{status: "error", wantDetected: false},
		{status: "partial", wantDetected: true},
		{status: "ok", wantDetected: true},
	} {
		t.Run(test.status, func(t *testing.T) {
			if err := UpsertDetectorState(ctx, db, DetectorState{Status: test.status}); err != nil {
				t.Fatalf("set detector status: %v", err)
			}
			report, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15"})
			if err != nil {
				t.Fatalf("ReadRecurring() error: %v", err)
			}
			foundManual := false
			foundDetected := false
			for _, item := range report.Items {
				foundManual = foundManual || item.Source == "manual"
				foundDetected = foundDetected || item.Source == "detected"
			}
			if !foundManual || foundDetected != test.wantDetected {
				t.Errorf("status %s rows = %+v, want manual and detected=%v",
					test.status, report.Items, test.wantDetected)
			}
		})
	}
}

func TestReadRecurringPartialProjectsEffectiveNextDateWithoutMutatingStoredLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if err := UpsertDetectorState(ctx, db, DetectorState{Status: "partial"}); err != nil {
		t.Fatalf("seed partial detector state: %v", err)
	}
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Active EOM Example", kind: "subscription",
		cadence: "monthly", expected: -1500, nextDate: "2026-01-31",
		source: "detected", active: true, detectKey: "active eom", amountSign: -1,
		lastMatchedCents: int64Pointer(-1500), anchor: 31, misses: 2,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Inactive EOM Example", kind: "subscription",
		cadence: "monthly", expected: -1500, nextDate: "2026-01-31",
		source: "detected", active: false, detectKey: "inactive eom", amountSign: -1,
		lastMatchedCents: int64Pointer(-1500), anchor: 31, misses: 2,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Manual EOM Example", kind: "subscription",
		cadence: "monthly", expected: -1500, nextDate: "2026-01-31",
		source: "manual", active: true,
	})

	report, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-03-05"})
	if err != nil {
		t.Fatalf("ReadRecurring(partial) error: %v", err)
	}
	byName := make(map[string]RecurringItem, len(report.Items))
	for _, item := range report.Items {
		byName[item.Name] = item
	}
	if byName["Active EOM Example"].NextExpectedDate != "2026-03-31" {
		t.Errorf("active projected date = %q, want 2026-03-31",
			byName["Active EOM Example"].NextExpectedDate)
	}
	if byName["Inactive EOM Example"].NextExpectedDate != "2026-01-31" ||
		byName["Manual EOM Example"].NextExpectedDate != "2026-01-31" {
		t.Errorf("inactive/manual dates were projected: %+v", byName)
	}
	var storedDate string
	var storedActive, storedMisses int
	if err := db.QueryRow(`
		SELECT next_expected_date, is_active, miss_count
		FROM recurring_items WHERE detect_key = 'active eom'
	`).Scan(&storedDate, &storedActive, &storedMisses); err != nil {
		t.Fatalf("read stored lifecycle after projection: %v", err)
	}
	if storedDate != "2026-01-31" || storedActive != 1 || storedMisses != 2 {
		t.Errorf("stored lifecycle mutated to %q/%d/%d", storedDate, storedActive, storedMisses)
	}
}

func TestReadRecurringDriftPercentExample(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if err := UpsertDetectorState(ctx, db, DetectorState{Status: "ok"}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Drift Example", kind: "subscription",
		cadence: "monthly", expected: -10000, nextDate: "2026-09-01",
		source: "detected", active: true, detectKey: "drift example", amountSign: -1,
		lastMatchedCents: int64Pointer(-12000), anchor: 1,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Threshold Example", kind: "subscription",
		cadence: "monthly", expected: -10000, nextDate: "2026-09-02",
		source: "detected", active: true, detectKey: "threshold example", amountSign: -1,
		lastMatchedCents: int64Pointer(-11000), anchor: 2,
	})

	report, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15"})
	if err != nil {
		t.Fatalf("ReadRecurring() error: %v", err)
	}
	byName := make(map[string]RecurringItem, len(report.Items))
	for _, item := range report.Items {
		byName[item.Name] = item
	}
	drift := byName["Drift Example"]
	if drift.DriftPercentX100 == nil || drift.DriftPercentX100.String() != "2000" || !drift.Drift {
		t.Errorf("drift = %v / %v, want 2000 hundredths and true",
			drift.DriftPercentX100, drift.Drift)
	}
	threshold := byName["Threshold Example"]
	if threshold.DriftPercentX100 == nil || threshold.DriftPercentX100.String() != "1000" || threshold.Drift {
		t.Errorf("threshold drift = %v / %v, want 1000 hundredths and false",
			threshold.DriftPercentX100, threshold.Drift)
	}
}

func TestReadRecurringDriftPercentUsesBigIntegerBoundaryMath(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if err := UpsertDetectorState(ctx, db, DetectorState{Status: "ok"}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Huge Positive Drift", kind: "income",
		cadence: "monthly", expected: 1, nextDate: "2026-09-01",
		source: "detected", active: true, detectKey: "huge positive", amountSign: 1,
		lastMatchedCents: int64Pointer(math.MaxInt64), anchor: 1,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Boundary Difference", kind: "subscription",
		cadence: "monthly", expected: math.MinInt64, nextDate: "2026-09-02",
		source: "detected", active: true, detectKey: "boundary difference", amountSign: -1,
		lastMatchedCents: int64Pointer(math.MaxInt64), anchor: 2,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Zero Expected", kind: "income",
		cadence: "monthly", expected: 0, nextDate: "2026-09-03",
		source: "detected", active: true, detectKey: "zero expected", amountSign: 1,
		lastMatchedCents: int64Pointer(100), anchor: 3,
	})
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "No Latest", kind: "income",
		cadence: "monthly", expected: 100, nextDate: "2026-09-04",
		source: "detected", active: true, detectKey: "no latest", amountSign: 1,
		anchor: 4,
	})

	report, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15"})
	if err != nil {
		t.Fatalf("ReadRecurring() error: %v", err)
	}
	byName := make(map[string]RecurringItem, len(report.Items))
	for _, item := range report.Items {
		byName[item.Name] = item
	}
	if got := byName["Huge Positive Drift"].DriftPercentX100; got == nil ||
		got.String() != "92233720368547758060000" || !byName["Huge Positive Drift"].Drift {
		t.Errorf("huge positive drift = %v / %v", got, byName["Huge Positive Drift"].Drift)
	}
	if got := byName["Boundary Difference"].DriftPercentX100; got == nil ||
		got.Sign() != 0 || byName["Boundary Difference"].Drift {
		t.Errorf("boundary drift = %v / %v, want zero/false", got, byName["Boundary Difference"].Drift)
	}
	for _, name := range []string{"Zero Expected", "No Latest"} {
		if byName[name].DriftPercentX100 != nil || byName[name].Drift {
			t.Errorf("%s drift = %v/%v, want null/false",
				name, byName[name].DriftPercentX100, byName[name].Drift)
		}
	}
}

func TestReadRecurringMonthlyEquivalentRejectsOverflow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if err := UpsertDetectorState(ctx, db, DetectorState{Status: "ok"}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	insertRecurringReadRow(t, db, recurringReadSeed{
		entityID: entityID, name: "Overflow Weekly Example", kind: "subscription",
		cadence: "weekly", expected: math.MaxInt64, nextDate: "2026-09-01",
		source: "detected", active: true, detectKey: "overflow weekly", amountSign: 1,
		lastMatchedCents: int64Pointer(math.MaxInt64), anchor: 1,
	})
	if _, err := ReadRecurring(ctx, db, RecurringFilter{AsOf: "2026-08-15"}); err == nil {
		t.Fatal("ReadRecurring() accepted overflowing weekly monthly equivalent")
	}
}

type recurringReadSeed struct {
	entityID         int64
	name             string
	kind             string
	cadence          string
	expected         int64
	nextDate         string
	source           string
	active           bool
	detectKey        string
	amountSign       int
	misses           int
	lastMatchedCents *int64
	anchor           int
}

func insertRecurringReadRow(t *testing.T, db *sql.DB, seed recurringReadSeed) int64 {
	t.Helper()
	active := 0
	if seed.active {
		active = 1
	}
	var nextDate any
	if seed.nextDate != "" {
		nextDate = seed.nextDate
	}
	var anchor any
	if seed.anchor != 0 {
		anchor = seed.anchor
	}
	result, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, miss_count,
			last_matched_cents, schedule_anchor_day
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		seed.entityID,
		seed.name,
		seed.kind,
		seed.cadence,
		seed.expected,
		nextDate,
		seed.source,
		active,
		seed.detectKey,
		seed.amountSign,
		seed.misses,
		seed.lastMatchedCents,
		anchor,
	)
	if err != nil {
		t.Fatalf("insert recurring read row %q: %v", seed.name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read recurring row id: %v", err)
	}
	return id
}

func int64Pointer(value int64) *int64 {
	return &value
}

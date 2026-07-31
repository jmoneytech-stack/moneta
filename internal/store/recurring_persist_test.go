package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/recurring"
)

func TestRecurringUpsertIdempotentAndCadenceUpdatesSameRow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	input := RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-08-01T12:00:00.000Z",
		Result: recurring.Result{Series: []recurring.Series{
			testDetectedSeries(entityID, "streambox example", "monthly", -1500),
		}},
	}
	if err := PersistRecurringDetection(ctx, db, input); err != nil {
		t.Fatalf("PersistRecurringDetection(first) error: %v", err)
	}

	updated := testDetectedSeries(entityID, "streambox example", "weekly", -1600)
	updated.Name = "Streambox Example Updated"
	updated.NextExpectedDate = "2026-08-08"
	updated.LastMatchedDate = "2026-08-01"
	updated.LastMatchedCents = -1700
	updated.ScheduleAnchorDay = 8
	input.RunAt = "2026-08-02T12:00:00.000Z"
	input.Result.Series = []recurring.Series{updated}
	if err := PersistRecurringDetection(ctx, db, input); err != nil {
		t.Fatalf("PersistRecurringDetection(second) error: %v", err)
	}

	var count int
	var name, cadence, nextDate, lastDate string
	var expected, lastCents int64
	var anchor, active, misses int
	var drift float64
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), name, cadence, expected_cents, next_expected_date,
			is_active, miss_count, last_matched_date, last_matched_cents,
			schedule_anchor_day, drift_pct
		FROM recurring_items
		WHERE source = 'detected'
	`).Scan(
		&count,
		&name,
		&cadence,
		&expected,
		&nextDate,
		&active,
		&misses,
		&lastDate,
		&lastCents,
		&anchor,
		&drift,
	); err != nil {
		t.Fatalf("read detected recurring row: %v", err)
	}
	if count != 1 || name != updated.Name || cadence != "weekly" || expected != -1600 ||
		nextDate != "2026-08-08" || active != 1 || misses != 0 ||
		lastDate != "2026-08-01" || lastCents != -1700 || anchor != 8 || drift != 0 {
		t.Errorf("updated recurring row = count %d / %q / %q / %d / %q / %d / %d / %q / %d / %d / %v",
			count, name, cadence, expected, nextDate, active, misses,
			lastDate, lastCents, anchor, drift)
	}
	state, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() error: %v", err)
	}
	if state.Status != "ok" || state.LastRunAt != input.RunAt ||
		state.LastSuccessAt != input.RunAt || state.LastSeriesCount != 1 {
		t.Errorf("detector state = %+v", state)
	}
}

func TestRecurringNeverTouchesManual(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, miss_count
		) VALUES (?, 'Manual Streambox', 'bill', 'quarterly', -9900,
			'2026-10-01', 'manual', 1, 'streambox example', -1, 7)
	`, entityID); err != nil {
		t.Fatalf("insert manual recurring row: %v", err)
	}
	series := testDetectedSeries(entityID, "streambox example", "monthly", -1500)
	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-08-01T12:00:00.000Z",
		Result:   recurring.Result{Series: []recurring.Series{series}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	var manualCount, detectedCount int
	var name, cadence, nextDate string
	var expected int64
	var active, misses int
	if err := db.QueryRowContext(ctx, `
		SELECT name, cadence, expected_cents, next_expected_date, is_active, miss_count
		FROM recurring_items WHERE source = 'manual'
	`).Scan(&name, &cadence, &expected, &nextDate, &active, &misses); err != nil {
		t.Fatalf("read manual recurring row: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT
			SUM(CASE WHEN source = 'manual' THEN 1 ELSE 0 END),
			SUM(CASE WHEN source = 'detected' THEN 1 ELSE 0 END)
		FROM recurring_items
	`).Scan(&manualCount, &detectedCount); err != nil {
		t.Fatalf("count recurring sources: %v", err)
	}
	if manualCount != 1 || detectedCount != 1 || name != "Manual Streambox" ||
		cadence != "quarterly" || expected != -9900 || nextDate != "2026-10-01" ||
		active != 1 || misses != 7 {
		t.Errorf("manual row changed: counts %d/%d, row %q/%q/%d/%q/%d/%d",
			manualCount, detectedCount, name, cadence, expected, nextDate, active, misses)
	}
}

func TestRecurringUnseenKeyDeactivatesOnSuccessfulDetect(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	insertDetectedRecurring(t, db, testDetectedSeries(entityID, "seen example", "monthly", -1500))
	insertDetectedRecurring(t, db, testDetectedSeries(entityID, "unseen example", "monthly", -2500))

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete:   true,
		RunAt:      "2026-08-01T12:00:00.000Z",
		Candidates: []recurring.Candidate{{EntityID: entityID}},
		Result: recurring.Result{Series: []recurring.Series{
			testDetectedSeries(entityID, "seen example", "monthly", -1500),
		}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	assertRecurringActive(t, db, entityID, "seen example", 1)
	assertRecurringActive(t, db, entityID, "unseen example", 0)
}

func TestRecurringOverflowSkipDoesNotDeactivateExistingSeries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	identity := recurring.Identity{EntityID: entityID, DetectKey: "overflow example", AmountSign: -1}
	insertDetectedRecurring(t, db, testDetectedSeries(entityID, identity.DetectKey, "monthly", -1500))

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete:   true,
		RunAt:      "2026-08-01T12:00:00.000Z",
		Candidates: []recurring.Candidate{{EntityID: entityID}},
		Result: recurring.Result{
			SkippedOverflow:    1,
			OverflowIdentities: []recurring.Identity{identity},
		},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	assertRecurringActive(t, db, entityID, identity.DetectKey, 1)
	state, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() error: %v", err)
	}
	if state.LastSkippedOverflow != 1 {
		t.Errorf("LastSkippedOverflow = %d, want 1", state.LastSkippedOverflow)
	}
}

func TestRecurringEntityWithOnlyActiveRowsStillDeactivatesUnseen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	insertDetectedRecurring(t, db, testDetectedSeries(entityID, "ended example", "monthly", -1500))

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-08-01T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	assertRecurringActive(t, db, entityID, "ended example", 0)
}

func TestRecurringPartialReactivatesOnlyActiveEligibleSeries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	inactive := testDetectedSeries(entityID, "reactivate example", "monthly", -1500)
	inactive.IsActive = false
	inactive.MissCount = 2
	insertDetectedRecurring(t, db, inactive)

	providerItemID := int64(42)
	candidates := []recurring.Candidate{
		{TransactionID: 10, EntityID: entityID, ProviderItemID: &providerItemID, Date: "2026-06-01", AmountCents: -1500},
		{TransactionID: 11, EntityID: entityID, ProviderItemID: &providerItemID, Date: "2026-07-01", AmountCents: -1500},
		{TransactionID: 12, EntityID: entityID, ProviderItemID: &providerItemID, Date: "2026-08-01", AmountCents: -1500},
	}
	reactivated := testDetectedSeries(entityID, "reactivate example", "monthly", -1500)
	reactivated.MemberTransactionIDs = []int64{10, 11, 12}
	inactiveNew := testDetectedSeries(entityID, "inactive new example", "monthly", -2500)
	inactiveNew.IsActive = false
	inactiveNew.MissCount = 2
	inactiveNew.MemberTransactionIDs = []int64{10, 11, 12}
	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete:                  false,
		RunAt:                     "2026-08-15T12:00:00.000Z",
		Candidates:                candidates,
		SuccessfulProviderItemIDs: map[int64]struct{}{providerItemID: {}},
		Result: recurring.Result{Series: []recurring.Series{
			reactivated,
			inactiveNew,
		}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection(partial) error: %v", err)
	}

	assertRecurringActive(t, db, entityID, "reactivate example", 1)
	var misses int
	if err := db.QueryRow(`
		SELECT miss_count FROM recurring_items WHERE detect_key = 'reactivate example'
	`).Scan(&misses); err != nil {
		t.Fatalf("read reactivated miss count: %v", err)
	}
	if misses != 0 {
		t.Errorf("reactivated miss count = %d, want 0", misses)
	}
	var inactiveNewCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM recurring_items WHERE detect_key = 'inactive new example'
	`).Scan(&inactiveNewCount); err != nil {
		t.Fatalf("count inactive partial insert: %v", err)
	}
	if inactiveNewCount != 0 {
		t.Errorf("inactive partial insert count = %d, want 0", inactiveNewCount)
	}
}

func TestRecurringPersistenceFailureRollsBackSeriesAndState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	before := DetectorState{
		Status: "ok", LastRunAt: "2026-08-01T12:00:00.000Z",
		LastSuccessAt: "2026-08-01T12:00:00.000Z", LastSeriesCount: 2,
	}
	if err := UpsertDetectorState(ctx, db, before); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-08-15T12:00:00.000Z",
		Result: recurring.Result{
			Series:          []recurring.Series{testDetectedSeries(entityID, "rollback example", "monthly", -1500)},
			SkippedOverflow: -1,
		},
	}); err == nil {
		t.Fatal("PersistRecurringDetection() succeeded with invalid detector state")
	}
	var seriesCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM recurring_items").Scan(&seriesCount); err != nil {
		t.Fatalf("count rolled-back recurring rows: %v", err)
	}
	if seriesCount != 0 {
		t.Errorf("recurring rows after failed persistence = %d, want 0", seriesCount)
	}
	after, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() after rollback: %v", err)
	}
	if after != before {
		t.Errorf("detector state after rollback = %+v, want %+v", after, before)
	}
}

func testDetectedSeries(entityID int64, detectKey, cadence string, expected int64) recurring.Series {
	return recurring.Series{
		EntityID:             entityID,
		DetectKey:            detectKey,
		AmountSign:           -1,
		Name:                 "Streambox Example",
		Kind:                 "subscription",
		Cadence:              cadence,
		ExpectedCents:        expected,
		NextExpectedDate:     "2026-09-01",
		IsActive:             true,
		LastMatchedDate:      "2026-08-01",
		LastMatchedCents:     expected,
		ScheduleAnchorDay:    1,
		MemberTransactionIDs: []int64{1, 2, 3},
	}
}

func insertDetectedRecurring(t *testing.T, db *sql.DB, series recurring.Series) int64 {
	t.Helper()
	active := 0
	if series.IsActive {
		active = 1
	}
	result, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, miss_count,
			last_matched_date, last_matched_cents, schedule_anchor_day
		) VALUES (?, ?, ?, ?, ?, ?, 'detected', ?, ?, ?, ?, ?, ?, ?)
	`,
		series.EntityID,
		series.Name,
		series.Kind,
		series.Cadence,
		series.ExpectedCents,
		series.NextExpectedDate,
		active,
		series.DetectKey,
		series.AmountSign,
		series.MissCount,
		series.LastMatchedDate,
		series.LastMatchedCents,
		series.ScheduleAnchorDay,
	)
	if err != nil {
		t.Fatalf("insert detected recurring row: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read detected recurring id: %v", err)
	}
	return id
}

func assertRecurringActive(t *testing.T, db *sql.DB, entityID int64, detectKey string, want int) {
	t.Helper()
	var active int
	if err := db.QueryRow(`
		SELECT is_active FROM recurring_items
		WHERE entity_id = ? AND detect_key = ? AND source = 'detected'
	`, entityID, detectKey).Scan(&active); err != nil {
		t.Fatalf("read recurring active state for %q: %v", detectKey, err)
	}
	if active != want {
		t.Errorf("recurring %q active = %d, want %d", detectKey, active, want)
	}
}

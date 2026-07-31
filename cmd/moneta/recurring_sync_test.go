package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/recurring"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

var fixedDetectNow = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

func TestSyncDetectRunsOnceAfterAllItems(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID:  {batch: &canon.SyncBatch{NextCursor: "cursor-first"}},
		second.DatabaseID: {batch: &canon.SyncBatch{NextCursor: "cursor-second"}},
	}
	detectCalls := 0
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func(rows []recurring.Candidate, asOf string) (recurring.Result, error) {
			detectCalls++
			if len(rows) != 0 || asOf != "2026-08-15" {
				t.Errorf("detect input = %d rows / %s, want 0 / 2026-08-15", len(rows), asOf)
			}
			var advanced int
			if err := db.QueryRow(`
				SELECT COUNT(*) FROM provider_items WHERE sync_cursor <> ''
			`).Scan(&advanced); err != nil {
				t.Fatalf("read cursors inside detect seam: %v", err)
			}
			if advanced != 2 {
				t.Errorf("advanced cursors before detect = %d, want 2", advanced)
			}
			return recurring.Result{SkippedOverflow: 2}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	if err := syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second},
		[]store.ProviderItem{first, second},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	); err != nil {
		t.Fatalf("syncItemsWithDetection() error: %v (stderr %q)", err, stderr.String())
	}
	if detectCalls != 1 {
		t.Errorf("detect calls = %d, want 1", detectCalls)
	}
	state := readDetectorStateForTest(t, db)
	if state.Status != "ok" || state.LastSeriesCount != 0 || state.LastSkippedOverflow != 2 {
		t.Errorf("detector state = %+v, want complete ok with two overflow skips", state)
	}
	if !strings.Contains(stdout.String(), "recurring: 0 series, 2 overflow skipped") {
		t.Errorf("sync output missing recurring count and overflow: %q", stdout.String())
	}
}

func TestSyncTransientFailureCannotUseOldDurableOK(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID:  {batch: &canon.SyncBatch{NextCursor: "cursor-first"}},
		second.DatabaseID: {syncErr: errors.New("transient pull failure")},
	}
	detectCalls := 0
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			detectCalls++
			return recurring.Result{}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	err := syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second},
		[]store.ProviderItem{first, second},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	)
	if err == nil {
		t.Fatal("syncItemsWithDetection() succeeded, want item failure")
	}
	if detectCalls != 1 {
		t.Errorf("detect calls = %d, want 1 after one successful item", detectCalls)
	}
	var durable string
	if err := db.QueryRow(
		"SELECT status FROM provider_items WHERE id = ?", second.DatabaseID,
	).Scan(&durable); err != nil {
		t.Fatalf("read failed item durable status: %v", err)
	}
	if durable != "ok" {
		t.Errorf("failed item durable status = %q, want stale ok", durable)
	}
	if state := readDetectorStateForTest(t, db); state.Status != "partial" {
		t.Errorf("detector state = %+v, want partial from current attempt", state)
	}
}

func TestSyncPartialPositiveEvidenceUpsertsWithoutNegativeTransitions(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	firstOld := insertOwnedDetectCandidate(t, db, entityID, first.DatabaseID,
		"2026-06-01", -1500, "Streambox Example", "first-old")
	firstLatest := insertOwnedDetectCandidate(t, db, entityID, first.DatabaseID,
		"2026-07-01", -1550, "Streambox Example", "first-latest")
	secondLatest := insertOwnedDetectCandidate(t, db, entityID, second.DatabaseID,
		"2026-08-01", -1700, "Streambox Example", "second-latest")
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, miss_count,
			last_matched_date, last_matched_cents, schedule_anchor_day
		) VALUES (?, 'Old Name', 'subscription', 'monthly', -1500,
			'2026-08-01', 'detected', 1, 'streambox example', -1, 0,
			'2026-05-01', -1500, 1)
	`, entityID); err != nil {
		t.Fatalf("insert existing detected row: %v", err)
	}
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID:  {batch: &canon.SyncBatch{NextCursor: "cursor-first"}},
		second.DatabaseID: {syncErr: errors.New("pull failed")},
	}
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			return recurring.Result{Series: []recurring.Series{{
				EntityID:             entityID,
				DetectKey:            "streambox example",
				AmountSign:           -1,
				Name:                 "Streambox Example Updated",
				Kind:                 "subscription",
				Cadence:              "weekly",
				ExpectedCents:        -1600,
				NextExpectedDate:     "2026-08-22",
				IsActive:             false,
				MissCount:            3,
				LastMatchedDate:      "2026-08-01",
				LastMatchedCents:     -1700,
				ScheduleAnchorDay:    8,
				MemberTransactionIDs: []int64{firstOld, firstLatest, secondLatest},
			}}}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	_ = syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second},
		[]store.ProviderItem{first, second},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	)

	var name, cadence, nextDate, lastDate string
	var expected, lastCents int64
	var active, misses, anchor int
	if err := db.QueryRow(`
		SELECT name, cadence, expected_cents, next_expected_date,
			is_active, miss_count, last_matched_date, last_matched_cents,
			schedule_anchor_day
		FROM recurring_items
		WHERE source = 'detected' AND detect_key = 'streambox example'
	`).Scan(
		&name, &cadence, &expected, &nextDate, &active, &misses,
		&lastDate, &lastCents, &anchor,
	); err != nil {
		t.Fatalf("read partial recurring row: %v", err)
	}
	if name != "Streambox Example Updated" || cadence != "weekly" || expected != -1600 ||
		nextDate != "2026-08-22" || active != 1 || misses != 0 ||
		lastDate != "2026-07-01" || lastCents != -1550 || anchor != 8 {
		t.Errorf("partial recurring row = %q/%q/%d/%q/%d/%d/%q/%d/%d",
			name, cadence, expected, nextDate, active, misses, lastDate, lastCents, anchor)
	}
	if state := readDetectorStateForTest(t, db); state.Status != "partial" {
		t.Errorf("detector state = %+v, want partial", state)
	}
}

func TestSyncPartialLeavesFailedItemOnlySeriesUnchanged(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	memberIDs := []int64{
		insertOwnedDetectCandidate(t, db, entityID, second.DatabaseID,
			"2026-06-01", -2500, "Failed Item Example", "failed-one"),
		insertOwnedDetectCandidate(t, db, entityID, second.DatabaseID,
			"2026-07-01", -2500, "Failed Item Example", "failed-two"),
		insertOwnedDetectCandidate(t, db, entityID, second.DatabaseID,
			"2026-08-01", -2500, "Failed Item Example", "failed-three"),
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, miss_count,
			last_matched_date, last_matched_cents, schedule_anchor_day
		) VALUES (?, 'Original Failed Item', 'subscription', 'monthly', -2500,
			'2026-09-01', 'detected', 1, 'failed item example', -1, 1,
			'2026-08-01', -2500, 1)
	`, entityID); err != nil {
		t.Fatalf("insert failed-item recurring row: %v", err)
	}
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID:  {batch: &canon.SyncBatch{NextCursor: "cursor-first"}},
		second.DatabaseID: {syncErr: errors.New("pull failed")},
	}
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			series := recurring.Series{
				EntityID: entityID, DetectKey: "failed item example", AmountSign: -1,
				Name: "Should Not Apply", Kind: "bill", Cadence: "weekly",
				ExpectedCents: -3000, NextExpectedDate: "2026-08-22", IsActive: true,
				LastMatchedDate: "2026-08-01", LastMatchedCents: -2500,
				ScheduleAnchorDay: 8, MemberTransactionIDs: memberIDs,
			}
			return recurring.Result{Series: []recurring.Series{series}}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	_ = syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second}, []store.ProviderItem{first, second},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	)
	var name, cadence string
	var expected int64
	var misses int
	if err := db.QueryRow(`
		SELECT name, cadence, expected_cents, miss_count
		FROM recurring_items WHERE detect_key = 'failed item example'
	`).Scan(&name, &cadence, &expected, &misses); err != nil {
		t.Fatalf("read failed-item recurring row: %v", err)
	}
	if name != "Original Failed Item" || cadence != "monthly" || expected != -2500 || misses != 1 {
		t.Errorf("failed-item recurring row changed to %q/%q/%d/%d", name, cadence, expected, misses)
	}
}

func TestSyncScopedItemSuccessDoesNotPromoteGlobalOk(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	detectCalls := 0
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			detectCalls++
			return recurring.Result{}, nil
		},
	}
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID: {batch: &canon.SyncBatch{NextCursor: "cursor-first"}},
	}

	var stdout, stderr bytes.Buffer
	if err := syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second}, []store.ProviderItem{first},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	); err != nil {
		t.Fatalf("scoped sync error: %v", err)
	}
	if detectCalls != 1 || readDetectorStateForTest(t, db).Status != "partial" {
		t.Errorf("scoped detect calls/state = %d/%+v, want 1/partial",
			detectCalls, readDetectorStateForTest(t, db))
	}
	if !strings.Contains(stdout.String(), "recurring: partial (positive evidence only)") {
		t.Errorf("scoped output = %q", stdout.String())
	}
}

func TestSyncAllItemsFailSetsPartialNotStaleOk(t *testing.T) {
	db, cipher, first := newSyncTestDB(t)
	second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			is_active, detect_key, amount_sign
		) VALUES (?, 'Preserved Example', 'subscription', 'monthly', -1500,
			'detected', 1, 'preserved example', -1)
	`, entityID); err != nil {
		t.Fatalf("insert preserved recurring row: %v", err)
	}
	if err := store.UpsertDetectorState(context.Background(), db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-08-01T00:00:00Z",
		LastSuccessAt: "2026-08-01T00:00:00Z", LastSeriesCount: 7,
		LastSkippedOverflow: 2,
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	providers := map[int64]*fakeSyncProvider{
		first.DatabaseID:  {syncErr: errors.New("first failed")},
		second.DatabaseID: {syncErr: errors.New("second failed")},
	}
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			t.Fatal("detect called when no item synced")
			return recurring.Result{}, nil
		},
	}

	var stdout, stderr bytes.Buffer
	_ = syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{first, second}, []store.ProviderItem{first, second},
		providerBuilder(providers), false, deps, &stdout, &stderr,
	)
	state := readDetectorStateForTest(t, db)
	if state.Status != "partial" || state.LastRunAt != "2026-08-15T12:00:00.000Z" ||
		state.LastSuccessAt != "2026-08-01T00:00:00Z" || state.LastSeriesCount != 7 ||
		state.LastSkippedOverflow != 2 || state.LastError != "" {
		t.Errorf("all-failed detector state = %+v", state)
	}
	var active int
	if err := db.QueryRow(
		"SELECT is_active FROM recurring_items WHERE detect_key = 'preserved example'",
	).Scan(&active); err != nil {
		t.Fatalf("read preserved recurring row: %v", err)
	}
	if active != 1 {
		t.Errorf("preserved recurring active = %d, want 1", active)
	}
	if !strings.Contains(stdout.String(), "recurring: skipped (no item synced; see moneta status)") {
		t.Errorf("all-failed output = %q", stdout.String())
	}
}

func TestSyncSurvivesDetectionFailure(t *testing.T) {
	db, cipher, item := newSyncTestDB(t)
	if err := store.UpsertDetectorState(context.Background(), db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-08-01T00:00:00Z",
		LastSuccessAt: "2026-08-01T00:00:00Z", LastSeriesCount: 4,
		LastSkippedOverflow: 2,
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source,
			is_active, detect_key, amount_sign
		) VALUES (?, 'Last Good Example', 'subscription', 'monthly', -1500,
			'detected', 1, 'last good example', -1)
	`, entityID); err != nil {
		t.Fatalf("insert last-good recurring row: %v", err)
	}
	provider := &fakeSyncProvider{batch: &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-detect", Name: "Test Checking",
			Type: canon.AccountTypeChecking, Currency: "USD",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID: "detect-txn", AccountRef: "checking-detect",
			Date: "2026-08-01", AmountCents: -1500,
			MerchantRaw: "STREAMBOX EXAMPLE", MerchantDisplay: "Streambox Example",
			Status: canon.TxnStatusPosted, Currency: "USD",
		}},
		NextCursor: "cursor-detect",
	}}
	deps := syncDetectionDependencies{
		now: func() time.Time { return fixedDetectNow },
		detect: func([]recurring.Candidate, string) (recurring.Result, error) {
			return recurring.Result{}, errors.New("injected detector failure with private detail")
		},
	}

	var stdout, stderr bytes.Buffer
	err = syncItemsWithDetection(
		context.Background(), db, cipher,
		[]store.ProviderItem{item}, []store.ProviderItem{item},
		providerBuilder(map[int64]*fakeSyncProvider{item.DatabaseID: provider}),
		false, deps, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("sync returned detection failure: %v", err)
	}
	var transactions int
	if err := db.QueryRow("SELECT COUNT(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatalf("count ingested transactions: %v", err)
	}
	if transactions != 1 {
		t.Errorf("ingested transactions = %d, want 1", transactions)
	}
	var active int
	if err := db.QueryRow(`
		SELECT is_active FROM recurring_items WHERE detect_key = 'last good example'
	`).Scan(&active); err != nil {
		t.Fatalf("read last-good recurring row: %v", err)
	}
	if active != 1 {
		t.Errorf("last-good recurring active = %d, want preserved 1", active)
	}
	state := readDetectorStateForTest(t, db)
	if state.Status != "error" || state.LastSuccessAt != "2026-08-01T00:00:00Z" ||
		state.LastSeriesCount != 4 || state.LastSkippedOverflow != 2 ||
		state.LastError != "recurring detection failed" {
		t.Errorf("failed detector state = %+v", state)
	}
	if strings.Contains(state.LastError, "private") ||
		!strings.Contains(stdout.String(), "recurring: detection failed") {
		t.Errorf("detection failure output/state = %q / %+v", stdout.String(), state)
	}
}

func TestDetectRunsOnEmptyBatchWhenComplete(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		db, cipher, item := newSyncTestDB(t)
		calls := 0
		deps := syncDetectionDependencies{
			now: func() time.Time { return fixedDetectNow },
			detect: func([]recurring.Candidate, string) (recurring.Result, error) {
				calls++
				return recurring.Result{}, nil
			},
		}
		provider := &fakeSyncProvider{batch: &canon.SyncBatch{}}
		var stdout, stderr bytes.Buffer
		if err := syncItemsWithDetection(
			context.Background(), db, cipher,
			[]store.ProviderItem{item}, []store.ProviderItem{item},
			providerBuilder(map[int64]*fakeSyncProvider{item.DatabaseID: provider}),
			false, deps, &stdout, &stderr,
		); err != nil {
			t.Fatalf("empty complete sync error: %v", err)
		}
		if calls != 1 || readDetectorStateForTest(t, db).Status != "ok" {
			t.Errorf("empty complete calls/state = %d/%+v", calls, readDetectorStateForTest(t, db))
		}
	})

	t.Run("scoped remains partial", func(t *testing.T) {
		db, cipher, first := newSyncTestDB(t)
		second := addSyncTestItem(t, db, cipher, "item-second", "Second Bank")
		calls := 0
		deps := syncDetectionDependencies{
			now: func() time.Time { return fixedDetectNow },
			detect: func([]recurring.Candidate, string) (recurring.Result, error) {
				calls++
				return recurring.Result{}, nil
			},
		}
		provider := &fakeSyncProvider{batch: &canon.SyncBatch{}}
		var stdout, stderr bytes.Buffer
		if err := syncItemsWithDetection(
			context.Background(), db, cipher,
			[]store.ProviderItem{first, second}, []store.ProviderItem{first},
			providerBuilder(map[int64]*fakeSyncProvider{first.DatabaseID: provider}),
			false, deps, &stdout, &stderr,
		); err != nil {
			t.Fatalf("empty scoped sync error: %v", err)
		}
		if calls != 1 || readDetectorStateForTest(t, db).Status != "partial" {
			t.Errorf("empty scoped calls/state = %d/%+v", calls, readDetectorStateForTest(t, db))
		}
	})
}

func addSyncTestItem(
	t *testing.T,
	db *sql.DB,
	cipher *secret.Cipher,
	itemID string,
	institution string,
) store.ProviderItem {
	t.Helper()
	ciphertext, err := cipher.Seal([]byte("fake-access-token-" + itemID))
	if err != nil {
		t.Fatalf("encrypt access token for %s: %v", itemID, err)
	}
	if _, err := store.SaveProviderItem(context.Background(), db, store.ProviderItemSecret{
		Provider: plaidProviderName, ItemID: itemID, Institution: institution,
		AccessTokenCiphertext: ciphertext,
	}); err != nil {
		t.Fatalf("save provider item %s: %v", itemID, err)
	}
	item, err := store.GetProviderItem(context.Background(), db, plaidProviderName, itemID)
	if err != nil {
		t.Fatalf("load provider item %s: %v", itemID, err)
	}
	return item
}

func providerBuilder(
	providers map[int64]*fakeSyncProvider,
) func(store.ProviderItem, string) (canon.Provider, error) {
	return func(item store.ProviderItem, _ string) (canon.Provider, error) {
		provider := providers[item.DatabaseID]
		if provider == nil {
			return nil, fmt.Errorf("no fake provider for item %s", item.ItemID)
		}
		return provider, nil
	}
}

func insertOwnedDetectCandidate(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	providerItemID int64,
	date string,
	amount int64,
	merchant string,
	hash string,
) int64 {
	t.Helper()
	var accountID int64
	providerAccountID := fmt.Sprintf("detect-account-%d", providerItemID)
	err := db.QueryRow(`
		SELECT id FROM accounts WHERE provider = 'plaid' AND provider_account_id = ?
	`, providerAccountID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := db.Exec(`
			INSERT INTO accounts (
				entity_id, provider_item_id, type, name, provider, provider_account_id
			) VALUES (?, ?, 'checking', 'Detection Checking', 'plaid', ?)
		`, entityID, providerItemID, providerAccountID)
		if insertErr != nil {
			t.Fatalf("insert candidate account: %v", insertErr)
		}
		accountID, insertErr = result.LastInsertId()
		if insertErr != nil {
			t.Fatalf("read candidate account id: %v", insertErr)
		}
	} else if err != nil {
		t.Fatalf("find candidate account: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, merchant_raw,
			merchant_norm, merchant_display, status, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'posted', ?)
	`, accountID, entityID, date, amount, merchant, strings.ToLower(merchant), merchant, hash)
	if err != nil {
		t.Fatalf("insert detect candidate: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read detect candidate id: %v", err)
	}
	return id
}

func readDetectorStateForTest(t *testing.T, db *sql.DB) store.DetectorState {
	t.Helper()
	state, err := store.ReadDetectorState(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadDetectorState() error: %v", err)
	}
	return state
}

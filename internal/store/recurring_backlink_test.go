package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/recurring"
)

func TestBackLinkRekeyFromDeactivatedDetectedToNewSeries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccount(t, db, entityID, "backlink-rekey-account")
	oldSeries := testDetectedSeries(entityID, "old descriptor", "monthly", -1500)
	oldID := insertDetectedRecurring(t, db, oldSeries)
	memberID := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-07-01", -1500, "rekey-member", &oldID,
	)
	newSeries := testDetectedSeries(entityID, "streambox example", "monthly", -1500)
	newSeries.MemberTransactionIDs = []int64{memberID}

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-07-30T12:00:00.000Z",
		AsOf:     "2026-07-30",
		Candidates: []recurring.Candidate{{
			TransactionID: memberID, EntityID: entityID, Date: "2026-07-01",
		}},
		Result: recurring.Result{Series: []recurring.Series{newSeries}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	var oldActive int
	if err := db.QueryRow("SELECT is_active FROM recurring_items WHERE id = ?", oldID).Scan(&oldActive); err != nil {
		t.Fatalf("read old recurring row: %v", err)
	}
	var newID, linkedID int64
	if err := db.QueryRow(`
		SELECT id FROM recurring_items
		WHERE source = 'detected' AND detect_key = 'streambox example'
	`).Scan(&newID); err != nil {
		t.Fatalf("read new recurring row: %v", err)
	}
	if err := db.QueryRow("SELECT recurring_id FROM transactions WHERE id = ?", memberID).Scan(&linkedID); err != nil {
		t.Fatalf("read re-keyed transaction: %v", err)
	}
	if oldActive != 0 || linkedID != newID {
		t.Errorf("re-key result = old active %d / linked %d, want 0 / %d", oldActive, linkedID, newID)
	}
}

func TestBackLinkPreservesManualOtherActivePreLookbackAndUnrelatedEndedSeriesLinks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	accountID := insertAccount(t, db, entityID, "backlink-protection-account")
	manualID := insertManualRecurring(t, db, entityID, "Manual Example")
	target := testDetectedSeries(entityID, "target example", "monthly", -1500)
	targetID := insertDetectedRecurring(t, db, target)
	other := testDetectedSeries(entityID, "other active example", "monthly", -2500)
	otherID := insertDetectedRecurring(t, db, other)
	ended := testDetectedSeries(entityID, "unrelated ended example", "monthly", -3500)
	ended.IsActive = false
	endedID := insertDetectedRecurring(t, db, ended)

	manualMember := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-06-01", -1500, "manual-member", &manualID,
	)
	otherActiveMember := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-07-01", -1500, "other-member", &otherID,
	)
	nullMember := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-07-15", -1500, "null-member", nil,
	)
	preLookback := insertBacklinkTransaction(
		t, db, accountID, entityID, "2025-04-30", -1500, "pre-lookback", &targetID,
	)
	unrelatedEnded := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-06-15", -3500, "unrelated-ended", &endedID,
	)
	lookbackNonMember := insertBacklinkTransaction(
		t, db, accountID, entityID, "2026-06-20", -1500, "target-non-member", &targetID,
	)
	target.MemberTransactionIDs = []int64{manualMember, otherActiveMember, nullMember}
	other.MemberTransactionIDs = []int64{999001, 999002, 999003}

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete: true,
		RunAt:    "2026-07-30T12:00:00.000Z",
		AsOf:     "2026-07-30",
		Candidates: []recurring.Candidate{
			{TransactionID: manualMember, EntityID: entityID, Date: "2026-06-01"},
			{TransactionID: otherActiveMember, EntityID: entityID, Date: "2026-07-01"},
			{TransactionID: nullMember, EntityID: entityID, Date: "2026-07-15"},
		},
		Result: recurring.Result{Series: []recurring.Series{target, other}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection() error: %v", err)
	}

	assertTransactionRecurringID(t, db, manualMember, &manualID)
	assertTransactionRecurringID(t, db, otherActiveMember, &otherID)
	assertTransactionRecurringID(t, db, nullMember, &targetID)
	assertTransactionRecurringID(t, db, preLookback, &targetID)
	assertTransactionRecurringID(t, db, unrelatedEnded, &endedID)
	assertTransactionRecurringID(t, db, lookbackNonMember, nil)
	assertRecurringActive(t, db, entityID, "other active example", 1)
	assertRecurringActive(t, db, entityID, "unrelated ended example", 0)
}

func TestBackLinkPartialModeOnlyAddsSuccessfulItemMembersAndNeverUnlinks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")
	successItemID := int64(101)
	failedItemID := int64(202)
	successAccountID := insertProviderBacklinkAccount(t, db, entityID, successItemID, "partial-success")
	failedAccountID := insertProviderBacklinkAccount(t, db, entityID, failedItemID, "partial-failed")
	target := testDetectedSeries(entityID, "partial target example", "monthly", -1500)
	targetID := insertDetectedRecurring(t, db, target)
	manualID := insertManualRecurring(t, db, entityID, "Partial Manual Example")
	inactiveOther := testDetectedSeries(entityID, "partial inactive other", "monthly", -1500)
	inactiveOther.IsActive = false
	inactiveOtherID := insertDetectedRecurring(t, db, inactiveOther)

	successNull := insertBacklinkTransaction(
		t, db, successAccountID, entityID, "2026-05-01", -1500, "success-null", nil,
	)
	failedNull := insertBacklinkTransaction(
		t, db, failedAccountID, entityID, "2026-06-01", -1500, "failed-null", nil,
	)
	successManual := insertBacklinkTransaction(
		t, db, successAccountID, entityID, "2026-07-01", -1500, "success-manual", &manualID,
	)
	successInactive := insertBacklinkTransaction(
		t, db, successAccountID, entityID, "2026-07-15", -1500, "success-inactive", &inactiveOtherID,
	)
	nonMember := insertBacklinkTransaction(
		t, db, successAccountID, entityID, "2026-06-15", -1500, "partial-non-member", &targetID,
	)
	target.MemberTransactionIDs = []int64{successNull, failedNull, successManual, successInactive}
	successful := map[int64]struct{}{successItemID: {}}

	if err := PersistRecurringDetection(ctx, db, RecurringDetectionInput{
		Complete:                  false,
		RunAt:                     "2026-07-30T12:00:00.000Z",
		AsOf:                      "2026-07-30",
		SuccessfulProviderItemIDs: successful,
		Candidates: []recurring.Candidate{
			backlinkCandidate(successNull, entityID, successItemID, "2026-05-01"),
			backlinkCandidate(failedNull, entityID, failedItemID, "2026-06-01"),
			backlinkCandidate(successManual, entityID, successItemID, "2026-07-01"),
			backlinkCandidate(successInactive, entityID, successItemID, "2026-07-15"),
		},
		Result: recurring.Result{Series: []recurring.Series{target}},
	}); err != nil {
		t.Fatalf("PersistRecurringDetection(partial) error: %v", err)
	}

	assertTransactionRecurringID(t, db, successNull, &targetID)
	assertTransactionRecurringID(t, db, failedNull, nil)
	assertTransactionRecurringID(t, db, successManual, &manualID)
	assertTransactionRecurringID(t, db, successInactive, &inactiveOtherID)
	assertTransactionRecurringID(t, db, nonMember, &targetID)
}

func insertManualRecurring(t *testing.T, db *sql.DB, entityID int64, name string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, source
		) VALUES (?, ?, 'subscription', 'monthly', -1500, 'manual')
	`, entityID, name)
	if err != nil {
		t.Fatalf("insert manual recurring row: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read manual recurring id: %v", err)
	}
	return id
}

func insertBacklinkTransaction(
	t *testing.T,
	db *sql.DB,
	accountID int64,
	entityID int64,
	date string,
	amount int64,
	hash string,
	recurringID *int64,
) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, merchant_raw,
			merchant_norm, merchant_display, status, dedup_hash, recurring_id
		) VALUES (?, ?, ?, ?, 'Streambox Example', 'streambox example',
			'Streambox Example', 'posted', ?, ?)
	`, accountID, entityID, date, amount, hash, recurringID)
	if err != nil {
		t.Fatalf("insert backlink transaction %q: %v", hash, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read backlink transaction id: %v", err)
	}
	return id
}

func insertProviderBacklinkAccount(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	providerItemID int64,
	providerAccountID string,
) int64 {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO provider_items (id, provider, item_id, access_token_enc)
		VALUES (?, 'plaid', ?, x'01')
	`, providerItemID, providerAccountID+"-item"); err != nil {
		t.Fatalf("insert backlink provider item: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, provider_item_id, type, name, provider, provider_account_id
		) VALUES (?, ?, 'checking', 'Backlink Checking', 'plaid', ?)
	`, entityID, providerItemID, providerAccountID)
	if err != nil {
		t.Fatalf("insert backlink provider account: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read backlink account id: %v", err)
	}
	return id
}

func backlinkCandidate(
	transactionID int64,
	entityID int64,
	providerItemID int64,
	date string,
) recurring.Candidate {
	return recurring.Candidate{
		TransactionID:  transactionID,
		EntityID:       entityID,
		ProviderItemID: &providerItemID,
		Date:           date,
		AmountCents:    -1500,
	}
}

func assertTransactionRecurringID(
	t *testing.T,
	db *sql.DB,
	transactionID int64,
	want *int64,
) {
	t.Helper()
	var got sql.NullInt64
	if err := db.QueryRow(
		"SELECT recurring_id FROM transactions WHERE id = ?", transactionID,
	).Scan(&got); err != nil {
		t.Fatalf("read transaction %d recurring id: %v", transactionID, err)
	}
	if want == nil {
		if got.Valid {
			t.Errorf("transaction %d recurring id = %d, want NULL", transactionID, got.Int64)
		}
		return
	}
	if !got.Valid || got.Int64 != *want {
		t.Errorf("transaction %d recurring id = %+v, want %d", transactionID, got, *want)
	}
}

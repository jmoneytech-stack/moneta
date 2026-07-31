package store

import (
	"context"
	"testing"
)

func TestLoadRecurringCandidatesFiltersAndCarriesProviderItem(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	providerItemID := saveTestItem(t, ctx, db, "plaid", "item-detect", "Fake Bank")
	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	var accountID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO accounts (
			entity_id, provider_item_id, type, name, provider, provider_account_id
		) VALUES (?, ?, 'checking', 'Test Checking', 'plaid', 'detect-account')
		RETURNING id
	`, entityID, providerItemID).Scan(&accountID); err != nil {
		t.Fatalf("insert candidate account: %v", err)
	}

	insertCandidate := func(
		date string,
		status string,
		excluded int,
		isTransfer int,
		hash string,
	) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents,
				merchant_raw, merchant_norm, merchant_display,
				category_id, status, excluded, is_transfer, dedup_hash
			) VALUES (?, ?, ?, -1500,
				'STREAMBOX EXAMPLE 1234', 'streambox example 1234', 'Streambox Example',
				6, ?, ?, ?, ?)
		`, accountID, entityID, date, status, excluded, isTransfer, hash); err != nil {
			t.Fatalf("insert candidate %q: %v", hash, err)
		}
	}
	insertCandidate("2026-07-01", "posted", 0, 0, "eligible")
	insertCandidate("2026-07-02", "pending", 0, 0, "pending")
	insertCandidate("2026-07-03", "posted", 1, 0, "excluded")
	insertCandidate("2026-07-04", "posted", 0, 1, "transfer")
	insertCandidate("2025-04-30", "posted", 0, 0, "before-lookback")
	insertCandidate("2026-07-31", "posted", 0, 0, "after-asof")

	var recurringBefore int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recurring_items").Scan(&recurringBefore); err != nil {
		t.Fatalf("count recurring items before candidate load: %v", err)
	}
	stateBefore, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() before candidate load: %v", err)
	}

	rows, err := LoadRecurringCandidates(ctx, db, "2026-07-30")
	if err != nil {
		t.Fatalf("LoadRecurringCandidates() error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("LoadRecurringCandidates() rows = %d, want 1: %+v", len(rows), rows)
	}
	candidate := rows[0]
	if candidate.EntityID != entityID || candidate.ProviderItemID == nil ||
		*candidate.ProviderItemID != providerItemID || candidate.Date != "2026-07-01" ||
		candidate.AmountCents != -1500 || candidate.MerchantDisplay != "Streambox Example" ||
		candidate.MerchantNorm != "streambox example 1234" ||
		candidate.MerchantRaw != "STREAMBOX EXAMPLE 1234" ||
		candidate.Category != "Entertainment" || candidate.Status != "posted" ||
		candidate.Excluded || candidate.IsTransfer {
		t.Errorf("loaded candidate = %+v", candidate)
	}

	var recurringAfter int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recurring_items").Scan(&recurringAfter); err != nil {
		t.Fatalf("count recurring items after candidate load: %v", err)
	}
	stateAfter, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() after candidate load: %v", err)
	}
	if recurringAfter != recurringBefore || stateAfter != stateBefore {
		t.Errorf("candidate loader wrote state: recurring %d -> %d, detector %+v -> %+v",
			recurringBefore, recurringAfter, stateBefore, stateAfter)
	}
}

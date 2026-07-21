package store

import (
	"context"
	"database/sql"
	"testing"
)

// saveTestItem stores one provider connection with a placeholder ciphertext
// blob; status reads never decrypt it. It returns the database row id.
func saveTestItem(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	provider string,
	itemID string,
	institution string,
) int64 {
	t.Helper()

	id, err := SaveProviderItem(ctx, db, ProviderItemSecret{
		Provider:              provider,
		ItemID:                itemID,
		Institution:           institution,
		AccessTokenCiphertext: []byte("placeholder-ciphertext"),
	})
	if err != nil {
		t.Fatalf("save provider item %q: %v", itemID, err)
	}
	return id
}

func TestListProviderItemStatusesEmpty(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	statuses, err := ListProviderItemStatuses(ctx, db)
	if err != nil {
		t.Fatalf("ListProviderItemStatuses() error: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("ListProviderItemStatuses() = %d rows, want 0", len(statuses))
	}
}

func TestListProviderItemStatusesCountsAndOrder(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	firstID := saveTestItem(t, ctx, db, "plaid", "item-b", "Bank B")
	secondID := saveTestItem(t, ctx, db, "plaid", "item-a", "Bank A")
	saveTestItem(t, ctx, db, "manual", "item-c", "")

	entityID, err := EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}

	var accountID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO accounts (
			entity_id, provider_item_id, type, name, institution,
			provider, provider_account_id
		) VALUES (?, ?, 'checking', 'Checking One', 'Bank A', 'plaid', 'acct-1')
		RETURNING id
	`, entityID, secondID).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, status, dedup_hash
		) VALUES (?, ?, '2026-07-20', -500, 'posted', 'hash-1')
	`, accountID, entityID); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE provider_items SET last_synced_at = '2026-07-20T14:03:11.123Z' WHERE id = ?",
		secondID,
	); err != nil {
		t.Fatalf("stamp last_synced_at: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE provider_items SET status = 'login_required' WHERE id = ?",
		firstID,
	); err != nil {
		t.Fatalf("set login_required: %v", err)
	}

	statuses, err := ListProviderItemStatuses(ctx, db)
	if err != nil {
		t.Fatalf("ListProviderItemStatuses() error: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("ListProviderItemStatuses() = %d rows, want 3", len(statuses))
	}

	wantOrder := []string{"item-c", "item-a", "item-b"}
	for i, want := range wantOrder {
		if statuses[i].ItemID != want {
			t.Errorf("statuses[%d].ItemID = %q, want %q", i, statuses[i].ItemID, want)
		}
	}

	manual := statuses[0]
	if manual.Provider != "manual" || manual.Institution != "" {
		t.Errorf("manual row = %+v, want provider manual with empty institution", manual)
	}

	synced := statuses[1]
	if synced.LastSyncedAt != "2026-07-20T14:03:11.123Z" {
		t.Errorf("LastSyncedAt = %q", synced.LastSyncedAt)
	}
	if synced.Accounts != 1 || synced.Transactions != 1 {
		t.Errorf("counts = %d accounts / %d transactions, want 1/1",
			synced.Accounts, synced.Transactions)
	}
	if synced.Status != "ok" {
		t.Errorf("Status = %q, want ok", synced.Status)
	}

	stale := statuses[2]
	if stale.Status != "login_required" {
		t.Errorf("Status = %q, want login_required", stale.Status)
	}
	if stale.LastSyncedAt != "" {
		t.Errorf("LastSyncedAt = %q, want empty for a never-synced item", stale.LastSyncedAt)
	}
	if stale.Accounts != 0 || stale.Transactions != 0 {
		t.Errorf("counts = %d/%d, want 0/0", stale.Accounts, stale.Transactions)
	}
}

func TestListProviderItemStatusesRequiresDB(t *testing.T) {
	if _, err := ListProviderItemStatuses(context.Background(), nil); err == nil {
		t.Fatal("ListProviderItemStatuses(nil) succeeded, want an error")
	}
}

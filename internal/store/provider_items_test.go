package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSaveProviderItemUpsertsSecretAndPreservesCursor(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	firstID, err := SaveProviderItem(ctx, db, ProviderItemSecret{
		Provider:              "plaid",
		ItemID:                "item-fake",
		Institution:           "First Sandbox Bank",
		AccessTokenCiphertext: []byte{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("SaveProviderItem() insert error: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE provider_items
		SET sync_cursor = 'cursor-keep', status = 'error'
		WHERE id = ?
	`, firstID); err != nil {
		t.Fatalf("prepare existing Item: %v", err)
	}

	secondID, err := SaveProviderItem(ctx, db, ProviderItemSecret{
		Provider:              "plaid",
		ItemID:                "item-fake",
		Institution:           "Updated Sandbox Bank",
		AccessTokenCiphertext: []byte{4, 5, 6},
	})
	if err != nil {
		t.Fatalf("SaveProviderItem() update error: %v", err)
	}
	if secondID != firstID {
		t.Errorf("updated id = %d, want %d", secondID, firstID)
	}

	var count int
	var institution, status, cursor string
	var ciphertext []byte
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), institution, access_token_enc, status, sync_cursor
		FROM provider_items
	`).Scan(&count, &institution, &ciphertext, &status, &cursor); err != nil {
		t.Fatalf("read provider Item: %v", err)
	}
	if count != 1 || institution != "Updated Sandbox Bank" || status != "ok" || cursor != "cursor-keep" {
		t.Errorf(
			"provider Item = count %d, institution %q, status %q, cursor %q",
			count,
			institution,
			status,
			cursor,
		)
	}
	if string(ciphertext) != string([]byte{4, 5, 6}) {
		t.Errorf("encrypted access token was not updated")
	}
}

func TestSaveProviderItemValidatesInput(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		useDB bool
		item  ProviderItemSecret
	}{
		{name: "database", item: ProviderItemSecret{Provider: "plaid", ItemID: "item", AccessTokenCiphertext: []byte{1}}},
		{name: "provider", useDB: true, item: ProviderItemSecret{ItemID: "item", AccessTokenCiphertext: []byte{1}}},
		{name: "item", useDB: true, item: ProviderItemSecret{Provider: "plaid", AccessTokenCiphertext: []byte{1}}},
		{name: "ciphertext", useDB: true, item: ProviderItemSecret{Provider: "plaid", ItemID: "item"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db = database
			if !test.useDB {
				db = nil
			}
			if _, err := SaveProviderItem(ctx, db, test.item); err == nil {
				t.Fatal("SaveProviderItem() succeeded with invalid input")
			}
		})
	}
}

func TestGetProviderItemLoadsEncryptedConnection(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	wantCiphertext := []byte{4, 5, 6}
	databaseID, err := SaveProviderItem(ctx, db, ProviderItemSecret{
		Provider:              "plaid",
		ItemID:                "item-fake",
		Institution:           "Test Bank",
		AccessTokenCiphertext: wantCiphertext,
	})
	if err != nil {
		t.Fatalf("SaveProviderItem() error: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE provider_items SET sync_cursor = 'cursor-1' WHERE id = ?
	`, databaseID); err != nil {
		t.Fatalf("set provider Item cursor: %v", err)
	}

	item, err := GetProviderItem(ctx, db, "plaid", "item-fake")
	if err != nil {
		t.Fatalf("GetProviderItem() error: %v", err)
	}
	if item.DatabaseID != databaseID || item.ItemID != "item-fake" ||
		item.Institution != "Test Bank" || item.SyncCursor != "cursor-1" {
		t.Errorf("GetProviderItem() = %#v", item)
	}
	if string(item.AccessTokenEnc) != string(wantCiphertext) {
		t.Errorf("encrypted access token = %v, want %v", item.AccessTokenEnc, wantCiphertext)
	}
}

func TestGetProviderItemValidatesInputAndReportsMissingItem(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		database *sql.DB
		provider string
		itemID   string
		missing  bool
	}{
		{name: "database", provider: "plaid", itemID: "item-fake"},
		{name: "provider", database: db, itemID: "item-fake"},
		{name: "item", database: db, provider: "plaid"},
		{name: "missing", database: db, provider: "plaid", itemID: "item-missing", missing: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := GetProviderItem(ctx, test.database, test.provider, test.itemID)
			if err == nil {
				t.Fatal("GetProviderItem() succeeded")
			}
			if test.missing && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("GetProviderItem() error = %v, want sql.ErrNoRows", err)
			}
		})
	}
}

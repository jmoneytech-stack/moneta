//go:build sandbox

package plaid

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/core"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestSandboxLinkedItemSyncAndIngest(t *testing.T) {
	if os.Getenv("MONETA_SANDBOX_LIVE") != "1" {
		t.Skip("set MONETA_SANDBOX_LIVE=1 to make live Plaid Sandbox calls")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	config, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("load Plaid Sandbox configuration: %v", err)
	}
	if config.environment != EnvironmentSandbox {
		t.Fatal("live integration test requires PLAID_ENV=sandbox")
	}
	cipher, err := secret.FromEnvironment()
	if err != nil {
		t.Fatalf("load secret cipher: %v", err)
	}
	databasePath := os.Getenv("MONETA_DB_PATH")
	if databasePath == "" {
		t.Fatal("MONETA_DB_PATH is required")
	}
	database, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open Sandbox database: %v", err)
	}
	defer database.Close()

	var providerItemID int64
	var itemID, institution, cursor string
	var ciphertext []byte
	err = database.QueryRowContext(ctx, `
		SELECT id, item_id, institution, access_token_enc, sync_cursor
		FROM provider_items
		WHERE provider = 'plaid'
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&providerItemID, &itemID, &institution, &ciphertext, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("no linked Plaid Item exists in the Sandbox database")
	}
	if err != nil {
		t.Fatalf("load linked Plaid Item: %v", err)
	}
	plaintext, err := cipher.Open(ciphertext)
	if err != nil {
		t.Fatalf("decrypt linked Plaid Item: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) || bytes.Contains(ciphertext, plaintext) {
		clear(plaintext)
		t.Fatal("Sandbox database contains a plaintext Plaid access token")
	}
	provider, err := New(config, itemID, institution, string(plaintext))
	clear(plaintext)
	if err != nil {
		t.Fatalf("create Plaid provider: %v", err)
	}

	connections, err := provider.Connections(ctx)
	if err != nil {
		t.Fatalf("read Plaid Item health: %v", err)
	}
	if len(connections) != 1 || connections[0].State != "ok" {
		t.Fatalf("Plaid Item health = %#v, want ok", connections)
	}

	var batch *canon.SyncBatch
	for attempt := 1; attempt <= 5; attempt++ {
		batch, err = provider.Sync(ctx, cursor)
		if errorCode(err) != "PRODUCT_NOT_READY" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		t.Fatalf("sync Plaid Sandbox Item: %v", err)
	}
	if len(batch.Accounts) == 0 {
		t.Fatal("Sandbox sync returned no accounts")
	}
	if len(batch.Balances) == 0 {
		t.Fatal("Sandbox sync returned no balances")
	}
	if batch.NextCursor == "" {
		t.Fatal("Sandbox sync returned an empty final cursor")
	}

	var entityID int64
	err = database.QueryRowContext(ctx, `
		INSERT INTO entities (kind, name)
		VALUES ('personal', 'Sandbox Personal')
		ON CONFLICT (kind, name) DO UPDATE SET
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id
	`).Scan(&entityID)
	if err != nil {
		t.Fatalf("create Sandbox entity: %v", err)
	}
	ingestor := core.NewIngestor(database)
	if _, err := ingestor.ApplySync(ctx, core.SyncTarget{
		ProviderItemID:  providerItemID,
		DefaultEntityID: entityID,
		ExpectedCursor:  cursor,
	}, batch); err != nil {
		t.Fatalf("ingest Plaid Sandbox batch: %v", err)
	}
	var storedCursor string
	if err := database.QueryRowContext(ctx, `
		SELECT sync_cursor
		FROM provider_items
		WHERE id = ?
	`, providerItemID).Scan(&storedCursor); err != nil {
		t.Fatalf("read stored Sandbox cursor: %v", err)
	}
	if storedCursor != batch.NextCursor {
		t.Fatal("Sandbox cursor was not advanced atomically")
	}

	var nonIntegerMoney, storedTransactions int
	if err := database.QueryRowContext(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE typeof(amount_cents) <> 'integer')
		FROM transactions
		WHERE account_id IN (
			SELECT id FROM accounts WHERE provider_item_id = ?
		)
	`, providerItemID).Scan(&storedTransactions, &nonIntegerMoney); err != nil {
		t.Fatalf("verify stored Sandbox transactions: %v", err)
	}
	if nonIntegerMoney != 0 {
		t.Fatalf("stored %d non-integer transaction amounts", nonIntegerMoney)
	}
	if storedTransactions == 0 {
		t.Fatal("Sandbox sync stored no transactions")
	}

	t.Logf(
		"Sandbox verified: accounts=%d transactions=%d balances=%d liabilities=%d",
		len(batch.Accounts),
		storedTransactions,
		len(batch.Balances),
		len(batch.Liabilities),
	)
}

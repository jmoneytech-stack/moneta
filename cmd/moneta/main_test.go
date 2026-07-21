package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestRunUsageAndUnknownCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantText string
	}{
		{name: "help", args: []string{"help"}, wantCode: 0, wantText: "usage: moneta"},
		{name: "missing", wantCode: 2, wantText: "usage: moneta"},
		{name: "unknown", args: []string{"unknown"}, wantCode: 2, wantText: "unknown command"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Errorf("run() code = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.wantText) {
				t.Errorf("run() output = %q, want %q", stdout.String()+stderr.String(), test.wantText)
			}
		})
	}
}

func TestRunLinkRequiresDatabasePathBeforeCredentials(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"link"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

func TestRunSyncRequiresDatabasePath(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sync"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

func TestSyncItemsAppliesBatchAndReportsSkips(t *testing.T) {
	db, cipher, item := newSyncTestDB(t)
	provider := &fakeSyncProvider{batch: &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID: "txn-1",
			AccountRef:    "checking-1",
			Date:          "2026-07-20",
			AmountCents:   -500,
			MerchantRaw:   "Coffee Shop",
			Status:        canon.TxnStatusPosted,
			Currency:      "USD",
		}},
		Skipped: []canon.SkippedRecord{{
			Kind:   canon.RecordKindTransaction,
			ID:     "eur-txn",
			Reason: canon.SkipUnsupportedCurrency,
			Detail: "EUR",
		}},
		NextCursor: "cursor-1",
	}}

	var stdout, stderr bytes.Buffer
	err := syncItems(
		context.Background(),
		db,
		cipher,
		[]store.ProviderItem{item},
		func(store.ProviderItem, string) (canon.Provider, error) {
			return provider, nil
		},
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("syncItems() error: %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Synced Test Bank: 1 record skipped.") {
		t.Errorf("syncItems() per-item output = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Synced 1 of 1 items, 1 record skipped.") {
		t.Errorf("syncItems() summary output = %q", stdout.String())
	}

	var cursor string
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		item.DatabaseID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read sync cursor: %v", err)
	}
	if cursor != "cursor-1" {
		t.Errorf("sync cursor = %q, want cursor-1", cursor)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM transactions").Scan(&count); err != nil {
		t.Fatalf("count transactions: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction count = %d, want 1", count)
	}
}

func TestSyncItemsReportsFailureWithoutAdvancingCursor(t *testing.T) {
	db, cipher, item := newSyncTestDB(t)
	provider := &fakeSyncProvider{syncErr: errors.New("sync failed")}

	var stdout, stderr bytes.Buffer
	err := syncItems(
		context.Background(),
		db,
		cipher,
		[]store.ProviderItem{item},
		func(store.ProviderItem, string) (canon.Provider, error) {
			return provider, nil
		},
		&stdout,
		&stderr,
	)
	if err == nil {
		t.Fatal("syncItems() succeeded, want an error")
	}
	if !strings.Contains(stderr.String(), "error: sync item item-fake: ") {
		t.Errorf("syncItems() stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Synced 0 of 1 items.") {
		t.Errorf("syncItems() summary output = %q", stdout.String())
	}

	var cursor string
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		item.DatabaseID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read sync cursor: %v", err)
	}
	if cursor != "" {
		t.Errorf("sync cursor = %q, want unchanged empty cursor", cursor)
	}
}

func TestSyncItemsWithoutLinkedItems(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := syncItems(context.Background(), nil, nil, nil, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("syncItems() error: %v", err)
	}
	if !strings.Contains(stdout.String(), "moneta link") {
		t.Errorf("syncItems() empty-state output = %q", stdout.String())
	}
}

func newSyncTestDB(t *testing.T) (*sql.DB, *secret.Cipher, store.ProviderItem) {
	t.Helper()

	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	ciphertext, err := cipher.Seal([]byte("access-fake-cli-token"))
	if err != nil {
		t.Fatalf("encrypt test access token: %v", err)
	}
	if _, err := store.SaveProviderItem(ctx, db, store.ProviderItemSecret{
		Provider:              plaidProviderName,
		ItemID:                "item-fake",
		Institution:           "Test Bank",
		AccessTokenCiphertext: ciphertext,
	}); err != nil {
		t.Fatalf("save provider Item: %v", err)
	}
	item, err := store.GetProviderItem(ctx, db, plaidProviderName, "item-fake")
	if err != nil {
		t.Fatalf("get provider Item: %v", err)
	}
	return db, cipher, item
}

type fakeSyncProvider struct {
	batch   *canon.SyncBatch
	syncErr error
}

func (p *fakeSyncProvider) Name() string {
	return plaidProviderName
}

func (p *fakeSyncProvider) Capabilities() canon.Capability {
	return canon.CapAccounts | canon.CapTransactions
}

func (p *fakeSyncProvider) Connections(context.Context) ([]canon.ConnectionStatus, error) {
	return nil, nil
}

func (p *fakeSyncProvider) Sync(context.Context, string) (*canon.SyncBatch, error) {
	return p.batch, p.syncErr
}

func TestRunLinkRejectsBroadListenAddress(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	t.Setenv("PLAID_CLIENT_ID", "client-fake")
	t.Setenv("PLAID_SECRET", "secret-fake")
	t.Setenv(
		"MONETA_ENCRYPTION_KEY",
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
	)
	var stdout, stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"link", "--listen", "0.0.0.0:0"},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "must listen on 127.0.0.1") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

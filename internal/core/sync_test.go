package core

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const fakeSyncAccessToken = "access-fake-sync-token"

func TestSyncProviderItemBootstrapsFreshDatabaseAndAppliesBatch(t *testing.T) {
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
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{13}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	ciphertext, err := cipher.Seal([]byte(fakeSyncAccessToken))
	if err != nil {
		t.Fatalf("encrypt test access token: %v", err)
	}
	if _, err := store.SaveProviderItem(ctx, db, store.ProviderItemSecret{
		Provider:              "plaid",
		ItemID:                "item-fake",
		Institution:           "Test Bank",
		AccessTokenCiphertext: ciphertext,
	}); err != nil {
		t.Fatalf("save provider Item: %v", err)
	}
	item, err := store.GetProviderItem(ctx, db, "plaid", "item-fake")
	if err != nil {
		t.Fatalf("get provider Item: %v", err)
	}

	provider := &fakeSyncProvider{batch: &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Institution:       "Test Bank",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Added: []canon.Transaction{{
			ProviderTxnID: "transaction-1",
			AccountRef:    "checking-1",
			Date:          "2026-07-18",
			AmountCents:   -1200,
			MerchantRaw:   "Coffee Shop",
			Status:        canon.TxnStatusPosted,
			Currency:      "USD",
		}},
		NextCursor: "cursor-1",
	}}
	var builtAccessToken string
	if _, err := SyncProviderItem(
		ctx,
		db,
		cipher,
		item,
		item.SyncCursor,
		item.SyncCursor,
		func(accessToken string) (canon.Provider, error) {
			builtAccessToken = accessToken
			return provider, nil
		},
	); err != nil {
		t.Fatalf("SyncProviderItem() error: %v", err)
	}
	if builtAccessToken != fakeSyncAccessToken {
		t.Fatal("provider builder did not receive the decrypted access token")
	}
	if provider.syncCursor != "" {
		t.Errorf("provider sync cursor = %q, want empty", provider.syncCursor)
	}

	var entityCount, accountCount, transactionCount, importRunCount int
	var amountCents int64
	var cursor string
	if err := db.QueryRow("SELECT count(*) FROM entities").Scan(&entityCount); err != nil {
		t.Fatalf("count bootstrapped entities: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM accounts").Scan(&accountCount); err != nil {
		t.Fatalf("count synced accounts: %v", err)
	}
	if err := db.QueryRow(
		"SELECT count(*), amount_cents FROM transactions",
	).Scan(&transactionCount, &amountCents); err != nil {
		t.Fatalf("read synced transactions: %v", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM import_runs").Scan(&importRunCount); err != nil {
		t.Fatalf("count import runs: %v", err)
	}
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		item.DatabaseID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read provider Item cursor: %v", err)
	}
	if entityCount != 1 || accountCount != 1 || transactionCount != 1 || importRunCount != 1 {
		t.Errorf(
			"stored counts = entities %d, accounts %d, transactions %d, imports %d",
			entityCount,
			accountCount,
			transactionCount,
			importRunCount,
		)
	}
	if amountCents != -1200 || cursor != "cursor-1" {
		t.Errorf("stored amount/cursor = %d/%q, want -1200/cursor-1", amountCents, cursor)
	}
}

func TestSyncProviderItemResetFailureLeavesStoredCursor(t *testing.T) {
	ctx := context.Background()
	db, cipher, item := newSyncTestItem(t)
	if _, err := db.ExecContext(ctx, `
		UPDATE provider_items SET sync_cursor = 'cursor-stored' WHERE id = ?
	`, item.DatabaseID); err != nil {
		t.Fatalf("set provider Item cursor: %v", err)
	}
	item.SyncCursor = "cursor-stored"
	provider := &fakeSyncProvider{syncErr: errors.New("pull failed")}

	_, err := SyncProviderItem(
		ctx,
		db,
		cipher,
		item,
		"",
		item.SyncCursor,
		func(string) (canon.Provider, error) { return provider, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "pull failed") {
		t.Fatalf("SyncProviderItem(reset failure) error = %v, want pull failed", err)
	}
	if provider.syncCursor != "" {
		t.Errorf("provider pull cursor = %q, want empty reset cursor", provider.syncCursor)
	}
	var stored string
	if err := db.QueryRowContext(ctx,
		"SELECT sync_cursor FROM provider_items WHERE id = ?", item.DatabaseID,
	).Scan(&stored); err != nil {
		t.Fatalf("read stored cursor: %v", err)
	}
	if stored != "cursor-stored" {
		t.Errorf("stored cursor after failed reset = %q, want cursor-stored", stored)
	}
}

func TestSyncProviderItemResetUpdatesHistoricalMerchantDisplayAndCursor(t *testing.T) {
	ctx := context.Background()
	db, cipher, item := newSyncTestItem(t)
	account := canon.Account{
		ProviderAccountID: "checking-1",
		Name:              "Test Checking",
		Type:              canon.AccountTypeChecking,
		Currency:          "USD",
	}
	historical := canon.Transaction{
		ProviderTxnID: "transaction-1",
		AccountRef:    "checking-1",
		Date:          "2026-07-18",
		AmountCents:   -1200,
		MerchantRaw:   "TST* CAFE 1234",
		Status:        canon.TxnStatusPosted,
		Currency:      "USD",
	}
	if _, err := SyncProviderItem(
		ctx,
		db,
		cipher,
		item,
		item.SyncCursor,
		item.SyncCursor,
		func(string) (canon.Provider, error) {
			return &fakeSyncProvider{batch: &canon.SyncBatch{
				Accounts:   []canon.Account{account},
				Added:      []canon.Transaction{historical},
				NextCursor: "cursor-stored",
			}}, nil
		},
	); err != nil {
		t.Fatalf("seed historical sync: %v", err)
	}
	item, err := store.GetProviderItem(ctx, db, "plaid", "item-fake")
	if err != nil {
		t.Fatalf("reload provider Item: %v", err)
	}

	enriched := historical
	enriched.MerchantDisplay = "Cafe Example"
	provider := &fakeSyncProvider{batch: &canon.SyncBatch{
		Modified:   []canon.Transaction{enriched},
		NextCursor: "cursor-replayed",
	}}
	if _, err := SyncProviderItem(
		ctx,
		db,
		cipher,
		item,
		"",
		item.SyncCursor,
		func(string) (canon.Provider, error) { return provider, nil },
	); err != nil {
		t.Fatalf("reset replay sync: %v", err)
	}
	if provider.syncCursor != "" {
		t.Errorf("provider replay cursor = %q, want empty", provider.syncCursor)
	}
	var display, cursor string
	if err := db.QueryRowContext(ctx, `
		SELECT transactions.merchant_display, provider_items.sync_cursor
		FROM transactions
		JOIN provider_items ON provider_items.id = ?
		WHERE transactions.id = (SELECT MIN(id) FROM transactions)
	`, item.DatabaseID).Scan(&display, &cursor); err != nil {
		t.Fatalf("read replay result: %v", err)
	}
	if display != "Cafe Example" || cursor != "cursor-replayed" {
		t.Errorf("replay result = display %q / cursor %q, want Cafe Example/cursor-replayed",
			display, cursor)
	}
}

func TestSyncProviderItemReturnsExistingCursorSentinel(t *testing.T) {
	ctx := context.Background()
	db, cipher, item := newSyncTestItem(t)
	if _, err := db.ExecContext(ctx, `
		UPDATE provider_items SET sync_cursor = 'cursor-concurrent' WHERE id = ?
	`, item.DatabaseID); err != nil {
		t.Fatalf("advance provider Item cursor: %v", err)
	}

	provider := &fakeSyncProvider{batch: &canon.SyncBatch{NextCursor: "cursor-next"}}
	_, err := SyncProviderItem(
		ctx,
		db,
		cipher,
		item,
		"",
		item.SyncCursor,
		func(string) (canon.Provider, error) {
			return provider, nil
		},
	)
	if err != ErrCursorChanged {
		t.Fatalf("SyncProviderItem() error = %v, want exact ErrCursorChanged", err)
	}
	if provider.syncCursor != "" {
		t.Errorf("provider reset cursor = %q, want empty", provider.syncCursor)
	}
}

func TestSyncProviderItemRejectsInvalidCiphertextBeforeProviderBuild(t *testing.T) {
	db, cipher, item := newSyncTestItem(t)
	item.AccessTokenEnc = []byte{1, 2, 3}
	builderCalled := false

	_, err := SyncProviderItem(
		context.Background(),
		db,
		cipher,
		item,
		item.SyncCursor,
		item.SyncCursor,
		func(string) (canon.Provider, error) {
			builderCalled = true
			return &fakeSyncProvider{}, nil
		},
	)
	if !errors.Is(err, secret.ErrCiphertextInvalid) {
		t.Fatalf("SyncProviderItem() error = %v, want ErrCiphertextInvalid", err)
	}
	if builderCalled {
		t.Fatal("invalid ciphertext reached the provider builder")
	}
}

func TestSyncProviderItemZeroizesPlaintextOnSuccessAndFailures(t *testing.T) {
	errBuild := errors.New("build failed")
	errSync := errors.New("sync failed")
	tests := []struct {
		name          string
		buildErr      error
		syncErr       error
		nilBatch      bool
		cursorChanged bool
		wantErr       error
		wantAnyErr    bool
	}{
		{name: "success"},
		{name: "build failure", buildErr: errBuild, wantErr: errBuild},
		{name: "sync failure", syncErr: errSync, wantErr: errSync},
		{name: "apply failure", nilBatch: true, wantAnyErr: true},
		{name: "cursor changed", cursorChanged: true, wantErr: ErrCursorChanged},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, _, item := newSyncTestItem(t)
			if test.cursorChanged {
				if _, err := db.ExecContext(ctx, `
					UPDATE provider_items SET sync_cursor = 'cursor-concurrent' WHERE id = ?
				`, item.DatabaseID); err != nil {
					t.Fatalf("advance provider Item cursor: %v", err)
				}
			}
			plaintext := []byte(fakeSyncAccessToken)
			batch := &canon.SyncBatch{NextCursor: "cursor-next"}
			if test.nilBatch {
				batch = nil
			}
			_, err := syncProviderItemWithPlaintext(
				ctx,
				db,
				item,
				item.SyncCursor,
				item.SyncCursor,
				plaintext,
				func(string) (canon.Provider, error) {
					if test.buildErr != nil {
						return nil, test.buildErr
					}
					return &fakeSyncProvider{batch: batch, syncErr: test.syncErr}, nil
				},
			)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Errorf("sync error = %v, want %v", err, test.wantErr)
			}
			if test.wantAnyErr && err == nil {
				t.Error("sync succeeded, want an error")
			}
			if test.wantErr == nil && !test.wantAnyErr && err != nil {
				t.Errorf("sync error = %v, want nil", err)
			}
			for index, value := range plaintext {
				if value != 0 {
					t.Fatalf("plaintext byte %d was not zeroized", index)
				}
			}
		})
	}
}

func TestSyncProviderItemSurfacesProviderAndIngestSkips(t *testing.T) {
	db, cipher, item := newSyncTestItem(t)
	provider := &fakeSyncProvider{batch: &canon.SyncBatch{
		Accounts: []canon.Account{{
			ProviderAccountID: "checking-1",
			Name:              "Test Checking",
			Type:              canon.AccountTypeChecking,
			Currency:          "USD",
		}},
		Liabilities: []canon.Liability{{
			AccountRef:      "checking-1",
			APR:             4.5,
			MinPaymentCents: centsPointer(2500),
		}},
		Skipped: []canon.SkippedRecord{{
			Kind:   canon.RecordKindTransaction,
			ID:     "eur-txn",
			Reason: canon.SkipUnsupportedCurrency,
			Detail: "EUR",
		}},
		NextCursor: "cursor-1",
	}}

	result, err := SyncProviderItem(
		context.Background(),
		db,
		cipher,
		item,
		item.SyncCursor,
		item.SyncCursor,
		func(string) (canon.Provider, error) {
			return provider, nil
		},
	)
	if err != nil {
		t.Fatalf("SyncProviderItem() error: %v", err)
	}
	if result == nil || len(result.Skipped) != 2 {
		t.Fatalf("SyncProviderItem() skipped = %#v, want two records", result)
	}
	providerSkip := result.Skipped[0]
	if providerSkip.Kind != canon.RecordKindTransaction ||
		providerSkip.Reason != canon.SkipUnsupportedCurrency || providerSkip.ID != "eur-txn" {
		t.Errorf("provider skip = %#v, want eur-txn unsupported_currency", providerSkip)
	}
	ingestSkip := result.Skipped[1]
	if ingestSkip.Kind != canon.RecordKindLiability ||
		ingestSkip.Reason != canon.SkipUnsupportedAccountType || ingestSkip.ID != "checking-1" {
		t.Errorf("ingest skip = %#v, want checking-1 liability unsupported_account_type", ingestSkip)
	}

	var cursor string
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		item.DatabaseID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read provider Item cursor: %v", err)
	}
	if cursor != "cursor-1" {
		t.Errorf("provider Item cursor = %q, want cursor-1", cursor)
	}
}

func newSyncTestItem(t *testing.T) (*sql.DB, *secret.Cipher, store.ProviderItem) {
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
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	ciphertext, err := cipher.Seal([]byte(fakeSyncAccessToken))
	if err != nil {
		t.Fatalf("encrypt test access token: %v", err)
	}
	if _, err := store.SaveProviderItem(ctx, db, store.ProviderItemSecret{
		Provider:              "plaid",
		ItemID:                "item-fake",
		Institution:           "Test Bank",
		AccessTokenCiphertext: ciphertext,
	}); err != nil {
		t.Fatalf("save provider Item: %v", err)
	}
	item, err := store.GetProviderItem(ctx, db, "plaid", "item-fake")
	if err != nil {
		t.Fatalf("get provider Item: %v", err)
	}
	return db, cipher, item
}

type fakeSyncProvider struct {
	batch      *canon.SyncBatch
	syncErr    error
	syncCursor string
}

func (p *fakeSyncProvider) Name() string {
	return "plaid"
}

func (p *fakeSyncProvider) Capabilities() canon.Capability {
	return canon.CapAccounts | canon.CapTransactions
}

func (p *fakeSyncProvider) Connections(context.Context) ([]canon.ConnectionStatus, error) {
	return nil, nil
}

func (p *fakeSyncProvider) Sync(_ context.Context, cursor string) (*canon.SyncBatch, error) {
	p.syncCursor = cursor
	return p.batch, p.syncErr
}

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

// SyncResult summarizes one orchestrated provider Item sync.
type SyncResult struct {
	// Skipped lists records dropped during provider normalization and ingest,
	// provider records first, with stable machine-readable reasons. It is
	// empty when the sync dropped nothing.
	Skipped []canon.SkippedRecord
}

// SyncProviderItem decrypts one stored credential, builds its provider, pulls
// the current cursor, bootstraps the Phase 1 entity, and atomically applies the
// returned batch. Decrypted credential bytes are cleared before return.
func SyncProviderItem(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	item store.ProviderItem,
	buildProvider func(accessToken string) (canon.Provider, error),
) (*SyncResult, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if cipher == nil {
		return nil, fmt.Errorf("secret cipher is required")
	}
	if item.DatabaseID <= 0 {
		return nil, fmt.Errorf("provider item database id must be positive")
	}
	if len(item.AccessTokenEnc) == 0 {
		return nil, fmt.Errorf("encrypted access token is required")
	}
	if buildProvider == nil {
		return nil, fmt.Errorf("provider builder is required")
	}

	plaintext, err := cipher.Open(item.AccessTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt provider item access token: %w", err)
	}
	return syncProviderItemWithPlaintext(ctx, db, item, plaintext, buildProvider)
}

func syncProviderItemWithPlaintext(
	ctx context.Context,
	db *sql.DB,
	item store.ProviderItem,
	plaintext []byte,
	buildProvider func(accessToken string) (canon.Provider, error),
) (*SyncResult, error) {
	defer clear(plaintext)

	provider, err := buildProvider(string(plaintext))
	if err != nil {
		return nil, fmt.Errorf("build provider: %w", err)
	}
	if provider == nil {
		return nil, fmt.Errorf("provider builder returned nil")
	}
	batch, err := provider.Sync(ctx, item.SyncCursor)
	if err != nil {
		return nil, fmt.Errorf("sync provider item: %w", err)
	}

	defaultEntityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		return nil, err
	}
	ingestResult, err := NewIngestor(db).ApplySync(ctx, SyncTarget{
		ProviderItemID:  item.DatabaseID,
		DefaultEntityID: defaultEntityID,
		ExpectedCursor:  item.SyncCursor,
	}, batch)
	if errors.Is(err, ErrCursorChanged) {
		return nil, ErrCursorChanged
	}
	if err != nil {
		return nil, fmt.Errorf("apply provider sync: %w", err)
	}

	result := &SyncResult{}
	result.Skipped = append(result.Skipped, batch.Skipped...)
	result.Skipped = append(result.Skipped, ingestResult.Skipped...)
	return result, nil
}

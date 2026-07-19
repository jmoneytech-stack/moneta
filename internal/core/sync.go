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

// SyncProviderItem decrypts one stored credential, builds its provider, pulls
// the current cursor, bootstraps the Phase 1 entity, and atomically applies the
// returned batch. Decrypted credential bytes are cleared before return.
func SyncProviderItem(
	ctx context.Context,
	db *sql.DB,
	cipher *secret.Cipher,
	item store.ProviderItem,
	buildProvider func(accessToken string) (canon.Provider, error),
) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if cipher == nil {
		return fmt.Errorf("secret cipher is required")
	}
	if item.DatabaseID <= 0 {
		return fmt.Errorf("provider item database id must be positive")
	}
	if len(item.AccessTokenEnc) == 0 {
		return fmt.Errorf("encrypted access token is required")
	}
	if buildProvider == nil {
		return fmt.Errorf("provider builder is required")
	}

	plaintext, err := cipher.Open(item.AccessTokenEnc)
	if err != nil {
		return fmt.Errorf("decrypt provider item access token: %w", err)
	}
	return syncProviderItemWithPlaintext(ctx, db, item, plaintext, buildProvider)
}

func syncProviderItemWithPlaintext(
	ctx context.Context,
	db *sql.DB,
	item store.ProviderItem,
	plaintext []byte,
	buildProvider func(accessToken string) (canon.Provider, error),
) error {
	defer clear(plaintext)

	provider, err := buildProvider(string(plaintext))
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}
	if provider == nil {
		return fmt.Errorf("provider builder returned nil")
	}
	batch, err := provider.Sync(ctx, item.SyncCursor)
	if err != nil {
		return fmt.Errorf("sync provider item: %w", err)
	}

	defaultEntityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		return err
	}
	err = NewIngestor(db).ApplySync(ctx, SyncTarget{
		ProviderItemID:  item.DatabaseID,
		DefaultEntityID: defaultEntityID,
		ExpectedCursor:  item.SyncCursor,
	}, batch)
	if errors.Is(err, ErrCursorChanged) {
		return ErrCursorChanged
	}
	if err != nil {
		return fmt.Errorf("apply provider sync: %w", err)
	}
	return nil
}

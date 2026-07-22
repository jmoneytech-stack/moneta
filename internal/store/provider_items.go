package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ProviderItemSecret is the encrypted provider credential and safe Item
// metadata persisted after a successful provider link flow.
type ProviderItemSecret struct {
	Provider              string
	ItemID                string
	Institution           string
	AccessTokenCiphertext []byte
}

// ProviderItem is the stored metadata and encrypted credential required to
// construct and sync one provider connection.
type ProviderItem struct {
	DatabaseID     int64
	ItemID         string
	Institution    string
	AccessTokenEnc []byte
	SyncCursor     string
}

// SaveProviderItem inserts or refreshes a provider Item without changing an
// existing sync cursor. The access token must already be encrypted.
func SaveProviderItem(
	ctx context.Context,
	db *sql.DB,
	item ProviderItemSecret,
) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("database is required")
	}
	if strings.TrimSpace(item.Provider) == "" {
		return 0, fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(item.ItemID) == "" {
		return 0, fmt.Errorf("provider item id is required")
	}
	if len(item.AccessTokenCiphertext) == 0 {
		return 0, fmt.Errorf("encrypted access token is required")
	}

	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc, status
		) VALUES (?, ?, ?, ?, 'ok')
		ON CONFLICT (provider, item_id) DO UPDATE SET
			institution = CASE
				WHEN excluded.institution <> '' THEN excluded.institution
				ELSE provider_items.institution
			END,
			access_token_enc = excluded.access_token_enc,
			status = 'ok',
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		RETURNING id
	`,
		item.Provider,
		item.ItemID,
		item.Institution,
		item.AccessTokenCiphertext,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save provider item: %w", err)
	}
	return id, nil
}

// SetProviderItemStatus updates the stored health status of one provider
// connection in its own short transaction (never inside a sync batch).
// Valid statuses are the schema set: ok, login_required, error.
func SetProviderItemStatus(
	ctx context.Context,
	db *sql.DB,
	provider string,
	itemID string,
	status string,
) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	switch status {
	case "ok", "login_required", "error":
	default:
		return fmt.Errorf("unsupported provider item status %q", status)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin provider item status update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE provider_items
		SET status = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE provider = ? AND item_id = ?
	`, status, provider, itemID)
	if err != nil {
		return fmt.Errorf("update provider item status: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read provider item status result: %w", err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("provider item %q is not linked", itemID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit provider item status update: %w", err)
	}
	return nil
}

// ListProviderItems loads every stored connection for one provider, ordered by
// item id, without decrypting or exposing credentials.
func ListProviderItems(
	ctx context.Context,
	db *sql.DB,
	provider string,
) ([]ProviderItem, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if strings.TrimSpace(provider) == "" {
		return nil, fmt.Errorf("provider is required")
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, item_id, institution, access_token_enc, sync_cursor
		FROM provider_items
		WHERE provider = ?
		ORDER BY item_id
	`, provider)
	if err != nil {
		return nil, fmt.Errorf("list provider items: %w", err)
	}
	defer rows.Close()

	var items []ProviderItem
	for rows.Next() {
		var item ProviderItem
		if err := rows.Scan(
			&item.DatabaseID,
			&item.ItemID,
			&item.Institution,
			&item.AccessTokenEnc,
			&item.SyncCursor,
		); err != nil {
			return nil, fmt.Errorf("scan provider item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list provider items: %w", err)
	}
	return items, nil
}

// GetProviderItem loads one provider connection without decrypting or exposing
// its credential.
func GetProviderItem(
	ctx context.Context,
	db *sql.DB,
	provider string,
	itemID string,
) (ProviderItem, error) {
	if db == nil {
		return ProviderItem{}, fmt.Errorf("database is required")
	}
	if strings.TrimSpace(provider) == "" {
		return ProviderItem{}, fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(itemID) == "" {
		return ProviderItem{}, fmt.Errorf("provider item id is required")
	}

	var item ProviderItem
	err := db.QueryRowContext(ctx, `
		SELECT id, item_id, institution, access_token_enc, sync_cursor
		FROM provider_items
		WHERE provider = ? AND item_id = ?
	`, provider, itemID).Scan(
		&item.DatabaseID,
		&item.ItemID,
		&item.Institution,
		&item.AccessTokenEnc,
		&item.SyncCursor,
	)
	if err != nil {
		return ProviderItem{}, fmt.Errorf("get provider item: %w", err)
	}
	return item, nil
}

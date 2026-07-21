package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ProviderItemStatus is the per-Item health and activity view behind
// 'moneta status'. It carries no credentials, amounts, or account names:
// institution names, coarse counts, and timestamps only.
//
// Status comes from provider_items.status. Successful syncs reset it to
// 'ok'; recording failed-run states such as 'login_required' is deferred
// to the Phase 2 service layer (see docs/moneta-plan.md), so a stored
// 'login_required' today can only come from an out-of-band write.
type ProviderItemStatus struct {
	Provider     string
	ItemID       string
	Institution  string
	Status       string
	LastSyncedAt string // RFC 3339, "" when the Item never synced
	Accounts     int
	Transactions int
}

// ListProviderItemStatuses loads every stored connection across all
// providers with per-Item account and transaction counts, ordered by
// provider then item id. Credentials are never selected.
func ListProviderItemStatuses(
	ctx context.Context,
	db *sql.DB,
) ([]ProviderItemStatus, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			provider,
			item_id,
			institution,
			status,
			COALESCE(last_synced_at, ''),
			(SELECT COUNT(*) FROM accounts
				WHERE accounts.provider_item_id = provider_items.id),
			(SELECT COUNT(*) FROM transactions
				JOIN accounts ON transactions.account_id = accounts.id
				WHERE accounts.provider_item_id = provider_items.id)
		FROM provider_items
		ORDER BY provider, item_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list provider item statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var statuses []ProviderItemStatus
	for rows.Next() {
		var status ProviderItemStatus
		if err := rows.Scan(
			&status.Provider,
			&status.ItemID,
			&status.Institution,
			&status.Status,
			&status.LastSyncedAt,
			&status.Accounts,
			&status.Transactions,
		); err != nil {
			return nil, fmt.Errorf("scan provider item status: %w", err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list provider item statuses: %w", err)
	}
	return statuses, nil
}

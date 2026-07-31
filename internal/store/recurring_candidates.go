package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/recurring"
)

// LoadRecurringCandidates reads the exact recurring-detection lookback using
// the binding analytics exclusions. It includes account ownership provenance
// for later partial-mode persistence and performs no writes.
func LoadRecurringCandidates(
	ctx context.Context,
	db *sql.DB,
	asOf string,
) ([]recurring.Candidate, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	start, end, err := recurring.Lookback(asOf)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			transactions.id,
			transactions.entity_id,
			accounts.provider_item_id,
			transactions.date,
			transactions.amount_cents,
			transactions.merchant_display,
			transactions.merchant_norm,
			transactions.merchant_raw,
			categories.name,
			transactions.status,
			transactions.excluded,
			transactions.is_transfer
		FROM transactions
		JOIN accounts ON accounts.id = transactions.account_id
		LEFT JOIN categories ON categories.id = transactions.category_id
		WHERE transactions.date >= ?
		  AND transactions.date <= ?
		  AND transactions.status = 'posted'
		  AND transactions.excluded = 0
		  AND transactions.is_transfer = 0
		ORDER BY transactions.entity_id, transactions.date, transactions.id
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("load recurring candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []recurring.Candidate
	for rows.Next() {
		var candidate recurring.Candidate
		var providerItemID sql.NullInt64
		var category sql.NullString
		if err := rows.Scan(
			&candidate.TransactionID,
			&candidate.EntityID,
			&providerItemID,
			&candidate.Date,
			&candidate.AmountCents,
			&candidate.MerchantDisplay,
			&candidate.MerchantNorm,
			&candidate.MerchantRaw,
			&category,
			&candidate.Status,
			&candidate.Excluded,
			&candidate.IsTransfer,
		); err != nil {
			return nil, fmt.Errorf("scan recurring candidate: %w", err)
		}
		if providerItemID.Valid {
			id := providerItemID.Int64
			candidate.ProviderItemID = &id
		}
		if category.Valid {
			candidate.Category = category.String
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load recurring candidates: %w", err)
	}
	return candidates, nil
}

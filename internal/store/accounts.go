package store

import (
	"context"
	"database/sql"
	"fmt"
)

// AccountSummary is the read model behind 'moneta accounts'. Balance is the
// account's latest balance snapshot in integer cents; it is nil when the
// account has never produced a snapshot. No credentials are selected.
type AccountSummary struct {
	ID            int64
	Name          string
	Type          string
	Institution   string
	EntityName    string
	Active        bool
	BalanceCents  *int64
	BalanceDate   string // YYYY-MM-DD of the latest snapshot, "" when none
}

// ListAccountSummaries loads accounts ordered by name then id, optionally
// restricted to one canonical account type ("" means all types).
func ListAccountSummaries(
	ctx context.Context,
	db *sql.DB,
	accountType string,
) ([]AccountSummary, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			accounts.id,
			accounts.name,
			accounts.type,
			accounts.institution,
			entities.name,
			accounts.is_active,
			(SELECT balance_snapshots.current_cents
				FROM balance_snapshots
				WHERE balance_snapshots.account_id = accounts.id
				ORDER BY balance_snapshots.date DESC
				LIMIT 1),
			COALESCE((SELECT balance_snapshots.date
				FROM balance_snapshots
				WHERE balance_snapshots.account_id = accounts.id
				ORDER BY balance_snapshots.date DESC
				LIMIT 1), '')
		FROM accounts
		JOIN entities ON entities.id = accounts.entity_id
		WHERE (? = '' OR accounts.type = ?)
		ORDER BY accounts.name, accounts.id
	`, accountType, accountType)
	if err != nil {
		return nil, fmt.Errorf("list account summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var accounts []AccountSummary
	for rows.Next() {
		var account AccountSummary
		var active int
		if err := rows.Scan(
			&account.ID,
			&account.Name,
			&account.Type,
			&account.Institution,
			&account.EntityName,
			&active,
			&account.BalanceCents,
			&account.BalanceDate,
		); err != nil {
			return nil, fmt.Errorf("scan account summary: %w", err)
		}
		account.Active = active == 1
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list account summaries: %w", err)
	}
	return accounts, nil
}

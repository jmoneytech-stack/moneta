package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TransactionFilter narrows 'moneta tx' reads. Zero values match
// everything. Dates are inclusive YYYY-MM-DD bounds; Account is a
// case-insensitive literal substring of the account name; Search is a
// case-insensitive literal substring matched against both the normalized
// and raw merchant text. LIKE metacharacters (%, _, \) in either value are
// escaped, so filters always mean literal text.
type TransactionFilter struct {
	From    string
	To      string
	Account string
	Search  string
}

// TransactionRow is one transaction read row. AmountCents is signed integer
// cents (negative = outflow). No provider payloads or credentials.
type TransactionRow struct {
	ID          int64
	Date        string
	AmountCents int64
	Merchant    string
	Status      string
	AccountName string
}

// TransactionSummary holds pre-computed aggregates over every transaction
// matching a filter, independent of any row-limit applied to the listing.
type TransactionSummary struct {
	Count        int
	TotalCents   int64 // signed sum; negative = net outflow
	InflowCents  int64
	OutflowCents int64 // negative or zero
}

const transactionFilterWhere = `
	FROM transactions
	JOIN accounts ON accounts.id = transactions.account_id
	WHERE (? = '' OR transactions.date >= ?)
	  AND (? = '' OR transactions.date <= ?)
	  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
	  AND (? = '' OR lower(transactions.merchant_norm) LIKE '%' || lower(?) || '%' ESCAPE '\'
	             OR lower(transactions.merchant_raw) LIKE '%' || lower(?) || '%' ESCAPE '\')
`

func filterArgs(filter TransactionFilter) []any {
	account := escapeLikeLiteral(filter.Account)
	search := escapeLikeLiteral(filter.Search)
	return []any{
		filter.From, filter.From,
		filter.To, filter.To,
		account, account,
		search, search, search,
	}
}

// escapeLikeLiteral escapes the LIKE metacharacters %, _, and \ so the
// filter value matches literal text only (paired with ESCAPE '\' above).
func escapeLikeLiteral(value string) string {
	if !strings.ContainsAny(value, `%_\`) {
		return value
	}
	var builder strings.Builder
	for _, r := range value {
		if r == '%' || r == '_' || r == '\\' {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// SummarizeTransactions aggregates every transaction matching filter.
func SummarizeTransactions(
	ctx context.Context,
	db *sql.DB,
	filter TransactionFilter,
) (TransactionSummary, error) {
	var summary TransactionSummary
	if db == nil {
		return summary, fmt.Errorf("database is required")
	}

	err := db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(transactions.amount_cents), 0),
			COALESCE(SUM(CASE WHEN transactions.amount_cents > 0
				THEN transactions.amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN transactions.amount_cents < 0
				THEN transactions.amount_cents ELSE 0 END), 0)
	`+transactionFilterWhere, filterArgs(filter)...).Scan(
		&summary.Count,
		&summary.TotalCents,
		&summary.InflowCents,
		&summary.OutflowCents,
	)
	if err != nil {
		return summary, fmt.Errorf("summarize transactions: %w", err)
	}
	return summary, nil
}

// ListTransactions loads up to limit transactions matching filter, newest
// first (date then id descending). A limit <= 0 returns every match; the
// caller is expected to pass --full in that case.
func ListTransactions(
	ctx context.Context,
	db *sql.DB,
	filter TransactionFilter,
	limit int,
) ([]TransactionRow, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}

	query := `
		SELECT
			transactions.id,
			transactions.date,
			transactions.amount_cents,
			CASE
				WHEN transactions.merchant_norm <> '' THEN transactions.merchant_norm
				ELSE transactions.merchant_raw
			END,
			transactions.status,
			accounts.name
	` + transactionFilterWhere + `
		ORDER BY transactions.date DESC, transactions.id DESC
	`
	args := filterArgs(filter)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var transactions []TransactionRow
	for rows.Next() {
		var transaction TransactionRow
		if err := rows.Scan(
			&transaction.ID,
			&transaction.Date,
			&transaction.AmountCents,
			&transaction.Merchant,
			&transaction.Status,
			&transaction.AccountName,
		); err != nil {
			return nil, fmt.Errorf("scan transaction: %w", err)
		}
		transactions = append(transactions, transaction)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	return transactions, nil
}

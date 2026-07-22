package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SpendFilter selects one inclusive period. From and To are required valid
// YYYY-MM-DD dates. Account is an optional case-insensitive literal
// substring of the account name; LIKE metacharacters are escaped.
type SpendFilter struct {
	From    string
	To      string
	Account string
}

// SpendSummary aggregates posted, non-excluded outflows in a period.
// SpendCents is positive presentation money even though source transaction
// outflows are stored as negative cents.
type SpendSummary struct {
	Count      int
	SpendCents int64
}

// SpendGroup is one category or merchant aggregate, ordered by spend
// descending then name.
type SpendGroup struct {
	Name       string
	Count      int
	SpendCents int64
}

// SpendReport is the complete read model behind 'moneta spend'. Group totals
// are independent of any row limit so CLI truncation is explicit.
type SpendReport struct {
	Summary       SpendSummary
	Categories    []SpendGroup
	CategoryTotal int
	Merchants     []SpendGroup
	MerchantTotal int
}

const spendFilterWhere = `
	FROM transactions
	JOIN accounts ON accounts.id = transactions.account_id
	WHERE transactions.date >= ?
	  AND transactions.date <= ?
	  AND transactions.status = 'posted'
	  AND transactions.excluded = 0
	  AND transactions.amount_cents < 0
	  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
`

func spendFilterArgs(filter SpendFilter) []any {
	account := escapeLikeLiteral(filter.Account)
	return []any{filter.From, filter.To, account, account}
}

func validateSpendFilter(filter SpendFilter) error {
	if filter.From == "" || filter.To == "" {
		return fmt.Errorf("spend filter requires from and to dates")
	}
	return validateTransactionFilter(TransactionFilter{
		From:    filter.From,
		To:      filter.To,
		Account: filter.Account,
	})
}

// ReadSpend returns a summary plus category and merchant breakdowns from one
// consistent read transaction. A limit <= 0 returns every group. Every money
// query applies the binding analytics rule (excluded = 0), includes posted
// rows only, and treats negative source amounts as positive spend.
func ReadSpend(
	ctx context.Context,
	db *sql.DB,
	filter SpendFilter,
	limit int,
) (SpendReport, error) {
	var report SpendReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	if err := validateSpendFilter(filter); err != nil {
		return report, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin spend read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(-transactions.amount_cents), 0)
	`+spendFilterWhere, spendFilterArgs(filter)...).Scan(
		&report.Summary.Count,
		&report.Summary.SpendCents,
	); err != nil {
		return report, fmt.Errorf("summarize spend: %w", err)
	}

	const categoryLabel = `CASE
		WHEN categories.id IS NULL THEN 'Uncategorized'
		ELSE categories.name
	END`
	report.Categories, report.CategoryTotal, err = listSpendGroups(
		ctx,
		tx,
		filter,
		limit,
		"LEFT JOIN categories ON categories.id = transactions.category_id",
		"categories.id",
		categoryLabel,
	)
	if err != nil {
		return report, fmt.Errorf("list spend by category: %w", err)
	}

	const merchantLabel = `CASE
		WHEN transactions.merchant_norm <> '' THEN transactions.merchant_norm
		WHEN transactions.merchant_raw <> '' THEN transactions.merchant_raw
		ELSE 'Unknown Merchant'
	END`
	report.Merchants, report.MerchantTotal, err = listSpendGroups(
		ctx,
		tx,
		filter,
		limit,
		"",
		merchantLabel,
		merchantLabel,
	)
	if err != nil {
		return report, fmt.Errorf("list spend by merchant: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit spend read: %w", err)
	}
	return report, nil
}

// listSpendGroups accepts SQL fragments from package-owned constants only;
// user input remains parameterized through spendFilterArgs. groupKey controls
// identity independently from the display label, so same-named categories
// remain distinct while merchant labels keep their existing grouping.
func listSpendGroups(
	ctx context.Context,
	tx *sql.Tx,
	filter SpendFilter,
	limit int,
	join string,
	groupKey string,
	label string,
) ([]SpendGroup, int, error) {
	base := spendFilterWhere
	if join != "" {
		base = "\n\tFROM transactions\n\tJOIN accounts ON accounts.id = transactions.account_id\n\t" + join + `
	WHERE transactions.date >= ?
	  AND transactions.date <= ?
	  AND transactions.status = 'posted'
	  AND transactions.excluded = 0
	  AND transactions.amount_cents < 0
	  AND (? = '' OR lower(accounts.name) LIKE '%' || lower(?) || '%' ESCAPE '\')
`
	}

	var total int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM (
			SELECT `+groupKey+` AS group_key, `+label+` AS group_name
		`+base+`
			GROUP BY group_key, group_name
		)
	`, spendFilterArgs(filter)...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count groups: %w", err)
	}

	query := `
		SELECT
			` + label + ` AS group_name,
			COUNT(*),
			COALESCE(SUM(-transactions.amount_cents), 0) AS spend_cents
	` + base + `
		GROUP BY ` + groupKey + `, group_name
		ORDER BY spend_cents DESC, group_name, ` + groupKey + `
	`
	args := spendFilterArgs(filter)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	groups := make([]SpendGroup, 0, min(total, max(limit, 0)))
	for rows.Next() {
		var group SpendGroup
		if err := rows.Scan(&group.Name, &group.Count, &group.SpendCents); err != nil {
			return nil, 0, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read groups: %w", err)
	}
	return groups, total, nil
}

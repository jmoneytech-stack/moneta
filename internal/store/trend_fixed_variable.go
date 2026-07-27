package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// fixedExpenseCategoryName is the entire heuristic-v1 rule: a category with
// exactly this name is fixed. Matching is by name only, so the rule does not
// depend on seeded category IDs surviving taxonomy edits.
const fixedExpenseCategoryName = "Rent and Utilities"

// TrendFixedVariableFilter selects one inclusive period and an optional
// literal account-name substring for the fixed-variable heuristic.
type TrendFixedVariableFilter struct {
	From    string
	To      string
	Account string
}

// TrendFixedVariableBucket is one static-heuristic spend bucket.
type TrendFixedVariableBucket struct {
	SpendCents int64
	Count      int
}

// TrendFixedVariableReport splits spend-filtered outflows into three complete,
// non-overlapping buckets. TotalCents and Count are the sums of those buckets.
type TrendFixedVariableReport struct {
	Fixed        TrendFixedVariableBucket
	Variable     TrendFixedVariableBucket
	Unclassified TrendFixedVariableBucket
	TotalCents   int64
	Count        int
}

// ReadTrendFixedVariable applies the v1 static category heuristic to the same
// posted, non-excluded outflows as ReadSpend. A NULL category is unclassified;
// the exact category name Rent and Utilities is fixed; every other categorized
// row that passed the spend filter is variable.
func ReadTrendFixedVariable(
	ctx context.Context,
	db *sql.DB,
	filter TrendFixedVariableFilter,
) (TrendFixedVariableReport, error) {
	var report TrendFixedVariableReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	spendFilter := SpendFilter(filter)
	if err := validateSpendFilter(spendFilter); err != nil {
		return report, fmt.Errorf("validate fixed-variable trend period: %w", err)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin fixed-variable trend read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT
			filtered.category_id,
			COALESCE(categories.name, ''),
			filtered.amount_cents
		FROM (
			SELECT transactions.id, transactions.category_id, transactions.amount_cents
	`+spendFilterWhere+`
		) AS filtered
		LEFT JOIN categories ON categories.id = filtered.category_id
		ORDER BY filtered.id
	`, spendFilterArgs(spendFilter)...)
	if err != nil {
		return report, fmt.Errorf("read fixed-variable trend transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var categoryID sql.NullInt64
		var categoryName string
		var amount int64
		if err := rows.Scan(&categoryID, &categoryName, &amount); err != nil {
			return report, fmt.Errorf("scan fixed-variable trend transaction: %w", err)
		}
		if amount == math.MinInt64 {
			return report, fmt.Errorf("spend magnitude overflows integer cents")
		}

		bucket := &report.Variable
		switch {
		case !categoryID.Valid:
			bucket = &report.Unclassified
		case categoryName == fixedExpenseCategoryName:
			bucket = &report.Fixed
		}
		if err := addTrendCents(&bucket.SpendCents, -amount); err != nil {
			return report, err
		}
		bucket.Count++
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read fixed-variable trend transactions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("close fixed-variable trend transactions: %w", err)
	}

	for _, bucket := range []TrendFixedVariableBucket{
		report.Fixed,
		report.Variable,
		report.Unclassified,
	} {
		if err := addTrendCents(&report.TotalCents, bucket.SpendCents); err != nil {
			return report, err
		}
		report.Count += bucket.Count
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit fixed-variable trend read: %w", err)
	}
	return report, nil
}

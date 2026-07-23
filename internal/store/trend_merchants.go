package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
)

const unknownTrendMerchant = "Unknown Merchant"

// TrendMerchantsFilter selects one inclusive period and an optional literal
// account-name substring for top-merchant spend.
type TrendMerchantsFilter struct {
	From    string
	To      string
	Account string
}

// TrendMerchant is one normalized-merchant spend aggregate.
type TrendMerchant struct {
	Name       string
	SpendCents int64
	Count      int
}

// TrendMerchantsReport contains full-period totals plus merchant rows.
// MerchantTotal is independent of any row limit.
type TrendMerchantsReport struct {
	SpendCents    int64
	Count         int
	Merchants     []TrendMerchant
	MerchantTotal int
}

type trendMerchantAggregate struct {
	key      string
	merchant TrendMerchant
}

// ReadTrendMerchants computes posted, non-excluded outflow totals grouped by
// merchant_norm. Empty normalized keys share one Unknown Merchant bucket. A
// limit <= 0 returns every merchant while preserving full summary totals.
func ReadTrendMerchants(
	ctx context.Context,
	db *sql.DB,
	filter TrendMerchantsFilter,
	limit int,
) (TrendMerchantsReport, error) {
	var report TrendMerchantsReport
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	spendFilter := SpendFilter(filter)
	if err := validateSpendFilter(spendFilter); err != nil {
		return report, fmt.Errorf("validate merchant trend period: %w", err)
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, fmt.Errorf("begin merchant trend read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT transactions.merchant_norm, transactions.amount_cents
	`+spendFilterWhere+`
		ORDER BY transactions.id
	`, spendFilterArgs(spendFilter)...)
	if err != nil {
		return report, fmt.Errorf("read merchant trend transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byMerchant := make(map[string]*trendMerchantAggregate)
	for rows.Next() {
		var key string
		var amount int64
		if err := rows.Scan(&key, &amount); err != nil {
			return report, fmt.Errorf("scan merchant trend transaction: %w", err)
		}
		if amount == math.MinInt64 {
			return report, fmt.Errorf("spend magnitude overflows integer cents")
		}
		spend := -amount
		aggregate := byMerchant[key]
		if aggregate == nil {
			name := key
			if name == "" {
				name = unknownTrendMerchant
			}
			aggregate = &trendMerchantAggregate{
				key:      key,
				merchant: TrendMerchant{Name: name},
			}
			byMerchant[key] = aggregate
		}
		if err := addTrendCents(&aggregate.merchant.SpendCents, spend); err != nil {
			return report, err
		}
		aggregate.merchant.Count++
		if err := addTrendCents(&report.SpendCents, spend); err != nil {
			return report, err
		}
		report.Count++
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read merchant trend transactions: %w", err)
	}

	aggregates := make([]trendMerchantAggregate, 0, len(byMerchant))
	for _, aggregate := range byMerchant {
		aggregates = append(aggregates, *aggregate)
	}
	sort.Slice(aggregates, func(left, right int) bool {
		if aggregates[left].merchant.SpendCents != aggregates[right].merchant.SpendCents {
			return aggregates[left].merchant.SpendCents > aggregates[right].merchant.SpendCents
		}
		if aggregates[left].merchant.Name != aggregates[right].merchant.Name {
			return aggregates[left].merchant.Name < aggregates[right].merchant.Name
		}
		return aggregates[left].key < aggregates[right].key
	})

	report.MerchantTotal = len(aggregates)
	shown := aggregates
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	report.Merchants = make([]TrendMerchant, 0, len(shown))
	for _, aggregate := range shown {
		report.Merchants = append(report.Merchants, aggregate.merchant)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit merchant trend read: %w", err)
	}
	return report, nil
}

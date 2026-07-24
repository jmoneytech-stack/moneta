package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

// DashboardFilter selects the inclusive transaction period the spend and
// cashflow sections cover. Balance sections are always as-of latest.
type DashboardFilter struct {
	From string
	To   string
}

// DashboardSync is the compact sync-health block. NeedsAttention counts every
// Item whose status is not 'ok'; LoginRequired is the reconnection subset that
// drives the CLI exit-3 signal. Per-Item detail stays in 'moneta status'.
type DashboardSync struct {
	Items          int
	NeedsAttention int
	LoginRequired  int
}

// DashboardReport composes existing reads into one content-first summary. It
// adds no aggregation of its own beyond selecting depository cash from the
// net-worth by-type totals and the credit-card portfolio utilization inputs.
//
// Upcoming bills and anomaly counts are deliberately absent: they are Phase 4
// inputs, and the render layer emits explicit null placeholders rather than
// fabricating values.
type DashboardReport struct {
	AsOf string // latest balance date across accounts, "" when none exists
	From string
	To   string

	Networth NetworthReport

	// Cash is the depository proxy: checking plus savings latest balances.
	CashCents    int64
	CashAccounts int

	// Card totals come from ReadCards, so loans are excluded.
	CardCount     int
	CardDebtCents int64

	// Portfolio utilization inputs, restricted to cards that have both a
	// balance snapshot and a positive limit. All zero means undefined.
	UtilizationCards      int
	UtilizationDebtCents  int64
	UtilizationLimitCents int64

	Spend    SpendSummary
	Cashflow CashflowSummary
	Sync     DashboardSync
}

// isDepositoryAccountType reports whether a canonical type counts as spendable
// cash. Investments and other assets are net worth but not cash.
func isDepositoryAccountType(accountType string) bool {
	switch canon.AccountType(accountType) {
	case canon.AccountTypeChecking, canon.AccountTypeSavings:
		return true
	default:
		return false
	}
}

// ReadDashboard composes ReadNetworth, ReadCards, ReadSpend, ReadCashflow, and
// ListProviderItemStatuses. Each underlying read owns its own consistency
// boundary, so the sections are individually consistent rather than one global
// snapshot; at personal-finance scale against a local file that is sufficient
// and avoids duplicating their SQL.
func ReadDashboard(
	ctx context.Context,
	db *sql.DB,
	filter DashboardFilter,
) (DashboardReport, error) {
	report := DashboardReport{From: filter.From, To: filter.To}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	if err := validateCashflowFilter(CashflowFilter{From: filter.From, To: filter.To}); err != nil {
		return report, fmt.Errorf("validate dashboard period: %w", err)
	}

	networth, err := ReadNetworth(ctx, db, NetworthFilter{})
	if err != nil {
		return report, fmt.Errorf("read dashboard net worth: %w", err)
	}
	report.Networth = networth
	report.AsOf = networth.AsOf
	for _, summary := range networth.ByType {
		if !isDepositoryAccountType(summary.Type) {
			continue
		}
		if err := addTrendCents(&report.CashCents, summary.BalanceCents); err != nil {
			return report, fmt.Errorf("total dashboard cash: %w", err)
		}
		report.CashAccounts += summary.BalancedCount
	}

	cards, err := ReadCards(ctx, db)
	if err != nil {
		return report, fmt.Errorf("read dashboard cards: %w", err)
	}
	report.CardCount = cards.Count
	report.CardDebtCents = cards.TotalDebtCents
	for _, card := range cards.Debts {
		if card.BalanceCents == nil || card.LimitCents == nil || *card.LimitCents <= 0 {
			continue
		}
		if err := addTrendCents(&report.UtilizationDebtCents, *card.BalanceCents); err != nil {
			return report, fmt.Errorf("total dashboard card debt: %w", err)
		}
		if err := addTrendCents(&report.UtilizationLimitCents, *card.LimitCents); err != nil {
			return report, fmt.Errorf("total dashboard card limits: %w", err)
		}
		report.UtilizationCards++
	}

	// The dashboard renders the spend summary only, so the group limit is
	// minimal; ReadSpend's summary totals are independent of any row limit.
	spend, err := ReadSpend(ctx, db, SpendFilter{From: filter.From, To: filter.To}, 1)
	if err != nil {
		return report, fmt.Errorf("read dashboard spend: %w", err)
	}
	report.Spend = spend.Summary

	cashflow, err := ReadCashflow(ctx, db, CashflowFilter{From: filter.From, To: filter.To})
	if err != nil {
		return report, fmt.Errorf("read dashboard cashflow: %w", err)
	}
	report.Cashflow = cashflow

	items, err := ListProviderItemStatuses(ctx, db)
	if err != nil {
		return report, fmt.Errorf("read dashboard sync health: %w", err)
	}
	report.Sync.Items = len(items)
	for _, item := range items {
		if item.Status != "ok" {
			report.Sync.NeedsAttention++
		}
		if item.Status == "login_required" {
			report.Sync.LoginRequired++
		}
	}
	return report, nil
}

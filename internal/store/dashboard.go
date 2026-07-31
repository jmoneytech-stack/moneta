package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

// DashboardFilter selects the inclusive transaction period for spend and
// cashflow. BillsAsOf is the local calendar date for bills and anomalies.
// Balance sections are always as-of latest.
type DashboardFilter struct {
	From      string
	To        string
	BillsAsOf string
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
// UpcomingBills is nil under never_run/error and non-nil under ok/partial.
// Anomalies always contains the default previous-complete-month read.
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

	// Portfolio utilization inputs come from ReadPortfolioUtilization, the
	// shared definition with the utilization trend: snapshot limit preferred
	// over credit_terms, overpaid balances floored to zero debt, cards with
	// no snapshot or no positive usable limit excluded. All zero is undefined.
	UtilizationCards      int
	UtilizationDebtCents  int64
	UtilizationLimitCents int64

	Spend           SpendSummary
	Cashflow        CashflowSummary
	Sync            DashboardSync
	RecurringDetect DetectorState
	UpcomingBills   *BillsReport
	Anomalies       AnomalyReport
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

// ReadDashboard composes ReadNetworth, ReadCards, ReadSpend, ReadCashflow,
// ReadBills, ReadAnomalies, and ListProviderItemStatuses. Each underlying read
// owns its own consistency boundary, so sections are individually consistent
// rather than one global snapshot. At personal-finance scale against a local
// file that is sufficient and avoids duplicating their SQL.
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

	portfolio, err := ReadPortfolioUtilization(ctx, db)
	if err != nil {
		return report, fmt.Errorf("read dashboard portfolio utilization: %w", err)
	}
	report.UtilizationCards = portfolio.Cards
	report.UtilizationDebtCents = portfolio.DebtCents
	report.UtilizationLimitCents = portfolio.LimitCents

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
	bills, err := ReadBills(ctx, db, filter.BillsAsOf, 30)
	if err != nil {
		return report, fmt.Errorf("read dashboard upcoming bills: %w", err)
	}
	report.RecurringDetect = bills.Detector
	if bills.Detector.Status == "ok" || bills.Detector.Status == "partial" {
		report.UpcomingBills = &bills
	}
	anomalies, err := ReadAnomalies(ctx, db, filter.BillsAsOf, "")
	if err != nil {
		return report, fmt.Errorf("read dashboard anomalies: %w", err)
	}
	report.Anomalies = anomalies
	return report, nil
}

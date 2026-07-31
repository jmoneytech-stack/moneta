// Package report builds the shared TOON/JSON documents that both the CLI and
// the REST mirror emit. A document lives here when the two surfaces are
// required to stay byte-for-byte identical, so there is one definition to
// change rather than two that can drift.
package report

import (
	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// cashNote states the deliberate narrowness of the cash proxy: investments and
// other assets reach net worth but are not spendable cash.
const cashNote = "checking + savings latest balances"

// Dashboard builds the content-first summary document. Both moneta dashboard
// and GET /v1/dashboard render exactly this, so their payloads cannot drift.
func Dashboard(dashboard store.DashboardReport) toon.Object {
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "as_of", Value: dashboardAsOf(dashboard)},
		}},
		{Key: "networth", Value: toon.Object{
			{Key: "assets", Value: cli.Money(dashboard.Networth.AssetsCents)},
			{Key: "liabilities", Value: cli.Money(dashboard.Networth.LiabilitiesCents)},
			{Key: "networth", Value: cli.Money(dashboard.Networth.NetworthCents)},
			{Key: "accounts", Value: dashboard.Networth.Accounts},
			{Key: "missing_balance", Value: dashboard.Networth.MissingBalance},
		}},
		{Key: "cash", Value: toon.Object{
			{Key: "balance", Value: cli.Money(dashboard.CashCents)},
			{Key: "accounts", Value: dashboard.CashAccounts},
			{Key: "note", Value: cashNote},
		}},
		{Key: "credit", Value: toon.Object{
			{Key: "utilization", Value: dashboardUtilization(dashboard)},
			{Key: "total_debt", Value: cli.Money(dashboard.CardDebtCents)},
			{Key: "cards", Value: dashboard.CardCount},
		}},
		{Key: "spend_month", Value: toon.Object{
			{Key: "from", Value: dashboard.From},
			{Key: "to", Value: dashboard.To},
			{Key: "total", Value: cli.Money(dashboard.Spend.SpendCents)},
			{Key: "count", Value: dashboard.Spend.Count},
		}},
		{Key: "cashflow_month", Value: toon.Object{
			{Key: "inflow", Value: cli.Money(dashboard.Cashflow.InflowCents)},
			{Key: "outflow", Value: cli.Money(dashboard.Cashflow.OutflowCents)},
			{Key: "net", Value: cli.Money(dashboard.Cashflow.NetCents)},
			{Key: "savings_rate", Value: dashboardSavingsRate(dashboard)},
			{Key: "count", Value: dashboard.Cashflow.Count},
		}},
		{Key: "sync", Value: toon.Object{
			{Key: "items", Value: dashboard.Sync.Items},
			{Key: "needs_attention", Value: dashboard.Sync.NeedsAttention},
			{Key: "login_required", Value: dashboard.Sync.LoginRequired},
		}},
		{Key: "recurring_detect", Value: Detector(dashboard.RecurringDetect, false)},
		{Key: "upcoming_bills", Value: dashboardUpcomingBills(dashboard)},
		{Key: "anomalies", Value: dashboardAnomalies(dashboard)},
		{Key: "hint", Value: dashboardHint(dashboard)},
	}
}

func dashboardAnomalies(dashboard store.DashboardReport) toon.Object {
	return toon.Object{
		{Key: "period", Value: dashboard.Anomalies.Period},
		{Key: "count", Value: len(dashboard.Anomalies.Items)},
		{Key: "top", Value: anomaliesTable(dashboard.Anomalies.Items, dashboardAnomaliesLimit)},
		{Key: "skipped_overflow", Value: dashboard.Anomalies.SkippedOverflow},
	}
}

func dashboardUpcomingBills(dashboard store.DashboardReport) any {
	if dashboard.UpcomingBills == nil {
		return nil
	}
	return toon.Object{
		{Key: "count", Value: len(dashboard.UpcomingBills.Items)},
		{Key: "bills", Value: billsTable(dashboard.UpcomingBills.Items, dashboardBillsLimit)},
	}
}

// dashboardAsOf is the latest balance date across accounts. It is null rather
// than a fabricated today when no account has a snapshot yet.
func dashboardAsOf(dashboard store.DashboardReport) any {
	if dashboard.AsOf == "" {
		return nil
	}
	return dashboard.AsOf
}

// dashboardUtilization is the credit-card portfolio ratio for "now", built
// from the shared store definition (snapshot limit preferred, overpaid
// balances floored to zero debt). It is null when no card qualifies, so a
// missing limit never reads as 0%.
func dashboardUtilization(dashboard store.DashboardReport) any {
	if dashboard.UtilizationCards == 0 {
		return nil
	}
	value := cli.Ratio(dashboard.UtilizationDebtCents, dashboard.UtilizationLimitCents, 4)
	if value == nil {
		return nil
	}
	return *value
}

func dashboardSavingsRate(dashboard store.DashboardReport) any {
	value := cli.Ratio(dashboard.Cashflow.NetCents, dashboard.Cashflow.InflowCents, 4)
	if value == nil {
		return nil
	}
	return *value
}

// dashboardHint is the single next step an agent should take.
func dashboardHint(dashboard store.DashboardReport) string {
	if dashboard.Sync.LoginRequired > 0 {
		return "re-run moneta link to reconnect items with status login_required"
	}
	if dashboard.Sync.Items == 0 {
		return "run moneta link to connect an institution, then moneta sync"
	}
	if dashboard.Networth.MissingBalance > 0 {
		return "run moneta sync to pull balances for accounts with no snapshot"
	}
	return "run moneta spend or moneta trends for the breakdown behind these totals"
}

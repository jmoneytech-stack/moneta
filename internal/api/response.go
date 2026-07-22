package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

func writeDocument(writer http.ResponseWriter, document toon.Object) {
	var buffer bytes.Buffer
	if err := cli.Render(&buffer, document, cli.FormatJSON); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal server error")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(buffer.Bytes())
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Error string `json:"error"`
	}{Error: message})
}

func buildStatusDocument(items []store.ProviderItemStatus, limit int, full bool) toon.Object {
	accounts := 0
	attention := 0
	for _, item := range items {
		accounts += item.Accounts
		if item.Status != "ok" {
			attention++
		}
	}
	shown := items
	if !full && len(items) > limit {
		shown = prioritizeStatusItems(items, limit)
	}
	table := toon.Table{
		Fields: []string{"provider", "item", "institution", "status", "accounts", "transactions", "last_sync"},
		Rows:   make([][]any, 0, len(shown)),
	}
	for _, item := range shown {
		table.Rows = append(table.Rows, []any{
			item.Provider, item.ItemID, item.Institution, item.Status,
			item.Accounts, item.Transactions, item.LastSyncedAt,
		})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "items", Value: len(items)},
			{Key: "accounts", Value: accounts},
			{Key: "needs_attention", Value: attention},
		}},
		{Key: "items", Value: table},
	}
	if len(shown) < len(items) {
		document = append(document, toon.Field{
			Key:   "truncated",
			Value: fmt.Sprintf("%d of %d shown (use full=true for all)", len(shown), len(items)),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: statusHint(items)})
}

func prioritizeStatusItems(items []store.ProviderItemStatus, limit int) []store.ProviderItemStatus {
	shown := make([]store.ProviderItemStatus, 0, limit)
	for _, item := range items {
		if item.Status != "ok" && len(shown) < limit {
			shown = append(shown, item)
		}
	}
	for _, item := range items {
		if item.Status == "ok" && len(shown) < limit {
			shown = append(shown, item)
		}
	}
	return shown
}

func statusHint(items []store.ProviderItemStatus) string {
	if len(items) == 0 {
		return "run moneta link to connect an institution, then moneta sync"
	}
	needsReconnect := false
	neverSynced := false
	for _, item := range items {
		if item.Status == "login_required" {
			needsReconnect = true
		}
		if item.LastSyncedAt == "" {
			neverSynced = true
		}
	}
	if needsReconnect {
		return "re-run moneta link to reconnect items with status login_required"
	}
	if neverSynced {
		return "run moneta sync to pull data for never-synced items"
	}
	return "run moneta sync to refresh balances and transactions"
}

func buildAccountsDocument(
	accounts []store.AccountSummary,
	typeFilter string,
	limit int,
	full bool,
) toon.Object {
	active := 0
	byType := toon.Object{}
	typeCounts := make(map[string]int)
	typeOrder := []string{}
	for _, account := range accounts {
		if account.Active {
			active++
		}
		if _, seen := typeCounts[account.Type]; !seen {
			typeOrder = append(typeOrder, account.Type)
		}
		typeCounts[account.Type]++
	}
	for _, accountType := range typeOrder {
		byType = append(byType, toon.Field{Key: accountType, Value: typeCounts[accountType]})
	}
	shown := accounts
	if !full && len(accounts) > limit {
		shown = accounts[:limit]
	}
	table := toon.Table{
		Fields: []string{"name", "type", "balance", "status"},
		Rows:   make([][]any, 0, len(shown)),
	}
	for _, account := range shown {
		balance := any(nil)
		if account.BalanceCents != nil {
			balance = cli.Money(*account.BalanceCents)
		}
		status := "active"
		if !account.Active {
			status = "inactive"
		}
		table.Rows = append(table.Rows, []any{account.Name, account.Type, balance, status})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "accounts", Value: len(accounts)},
			{Key: "active", Value: active},
			{Key: "by_type", Value: byType},
		}},
		{Key: "accounts", Value: table},
	}
	if len(shown) < len(accounts) {
		document = append(document, toon.Field{
			Key:   "truncated",
			Value: fmt.Sprintf("%d of %d shown (use full=true for all)", len(shown), len(accounts)),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: accountsHint(accounts, typeFilter)})
}

func accountsHint(accounts []store.AccountSummary, typeFilter string) string {
	if len(accounts) == 0 {
		if typeFilter != "" {
			return fmt.Sprintf("no accounts match type=%s; relax the filter or run moneta sync", typeFilter)
		}
		return "no accounts yet; run moneta link to connect an institution, then moneta sync"
	}
	for _, account := range accounts {
		if account.BalanceCents == nil {
			return "run moneta sync to pull balances for accounts without a snapshot"
		}
	}
	return "run moneta tx to inspect transactions; moneta status for sync health"
}

func buildTransactionsDocument(
	summary store.TransactionSummary,
	transactions []store.TransactionRow,
	filter store.TransactionFilter,
) toon.Object {
	table := toon.Table{
		Fields: []string{"date", "amount", "merchant", "status", "account"},
		Rows:   make([][]any, 0, len(transactions)),
	}
	for _, transaction := range transactions {
		table.Rows = append(table.Rows, []any{
			transaction.Date,
			cli.Money(transaction.AmountCents),
			transaction.Merchant,
			transaction.Status,
			transaction.AccountName,
		})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: summary.Count},
			{Key: "excluded_count", Value: summary.ExcludedCount},
			{Key: "total", Value: cli.Money(summary.TotalCents)},
			{Key: "inflow", Value: cli.Money(summary.InflowCents)},
			{Key: "outflow", Value: cli.Money(summary.OutflowCents)},
		}},
		{Key: "tx", Value: table},
	}
	if len(transactions) < summary.Count {
		document = append(document, toon.Field{
			Key: "truncated",
			Value: fmt.Sprintf(
				"%d of %d shown (use full=true for all)",
				len(transactions), summary.Count,
			),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: transactionsHint(summary.Count, filter)})
}

func transactionsHint(count int, filter store.TransactionFilter) string {
	if count > 0 {
		return "run moneta accounts for balances; moneta status for sync health"
	}
	if filter.From != "" || filter.To != "" || filter.Account != "" || filter.Search != "" {
		return "no matches; widen the date range or relax account/search filters"
	}
	return "no transactions yet; run moneta link to connect an institution, then moneta sync"
}

func buildSpendDocument(report store.SpendReport, filter store.SpendFilter) toon.Object {
	categories := toon.Table{
		Fields: []string{"category", "spend", "count"},
		Rows:   make([][]any, 0, len(report.Categories)),
	}
	for _, group := range report.Categories {
		categories.Rows = append(categories.Rows, []any{group.Name, cli.Money(group.SpendCents), group.Count})
	}
	merchants := toon.Table{
		Fields: []string{"merchant", "spend", "count"},
		Rows:   make([][]any, 0, len(report.Merchants)),
	}
	for _, group := range report.Merchants {
		merchants.Rows = append(merchants.Rows, []any{group.Name, cli.Money(group.SpendCents), group.Count})
	}
	document := toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "from", Value: filter.From},
			{Key: "to", Value: filter.To},
			{Key: "count", Value: report.Summary.Count},
			{Key: "total_spend", Value: cli.Money(report.Summary.SpendCents)},
		}},
		{Key: "by_category", Value: categories},
	}
	if len(report.Categories) < report.CategoryTotal {
		document = append(document, toon.Field{
			Key: "category_truncated",
			Value: fmt.Sprintf(
				"%d of %d groups shown (use full=true for all)",
				len(report.Categories), report.CategoryTotal,
			),
		})
	}
	document = append(document, toon.Field{Key: "by_merchant", Value: merchants})
	if len(report.Merchants) < report.MerchantTotal {
		document = append(document, toon.Field{
			Key: "merchant_truncated",
			Value: fmt.Sprintf(
				"%d of %d groups shown (use full=true for all)",
				len(report.Merchants), report.MerchantTotal,
			),
		})
	}
	return append(document, toon.Field{Key: "hint", Value: spendHint(report.Summary.Count, filter)})
}

func spendHint(count int, filter store.SpendFilter) string {
	if count == 0 {
		return "no posted spending in this period; widen period/from/to or run moneta sync"
	}
	return fmt.Sprintf(
		"run moneta tx --from %s --to %s to inspect ledger rows",
		filter.From, filter.To,
	)
}

func buildCashflowDocument(summary store.CashflowSummary, filter store.CashflowFilter) toon.Object {
	rate := any(nil)
	if value := savingsRateNumber(summary.NetCents, summary.InflowCents); value != nil {
		rate = *value
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "from", Value: filter.From},
			{Key: "to", Value: filter.To},
			{Key: "count", Value: summary.Count},
			{Key: "inflow", Value: cli.Money(summary.InflowCents)},
			{Key: "outflow", Value: cli.Money(summary.OutflowCents)},
			{Key: "net", Value: cli.Money(summary.NetCents)},
			{Key: "savings_rate", Value: rate},
		}},
		{Key: "hint", Value: cashflowHint(summary, filter)},
	}
}

func savingsRateNumber(netCents, inflowCents int64) *toon.Number {
	if inflowCents <= 0 {
		return nil
	}
	const decimalPlaces = 4
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(decimalPlaces), nil)
	numerator := new(big.Int).Mul(big.NewInt(netCents), scale)
	scaled := new(big.Int).Quo(numerator, big.NewInt(inflowCents))
	value := toon.Number(formatScaledInteger(scaled, decimalPlaces))
	return &value
}

func formatScaledInteger(value *big.Int, decimalPlaces int) string {
	negative := value.Sign() < 0
	magnitude := new(big.Int).Abs(new(big.Int).Set(value)).String()
	if len(magnitude) <= decimalPlaces {
		magnitude = strings.Repeat("0", decimalPlaces-len(magnitude)+1) + magnitude
	}
	split := len(magnitude) - decimalPlaces
	whole := magnitude[:split]
	fraction := strings.TrimRight(magnitude[split:], "0")
	formatted := whole
	if fraction != "" {
		formatted += "." + fraction
	}
	if negative && formatted != "0" {
		formatted = "-" + formatted
	}
	return formatted
}

func cashflowHint(summary store.CashflowSummary, filter store.CashflowFilter) string {
	if summary.Count == 0 {
		return "no posted cashflow in this period; widen period/from/to or run moneta sync"
	}
	if summary.InflowCents == 0 {
		return "savings_rate is null because inflow is zero; widen the period or inspect moneta tx"
	}
	return fmt.Sprintf(
		"run moneta spend --from %s --to %s to inspect outflows",
		filter.From, filter.To,
	)
}

func buildDebtsDocument(report store.DebtReport) toon.Object {
	table := toon.Table{
		Fields: []string{"name", "type", "balance", "limit", "utilization", "apr", "due_day"},
		Rows:   make([][]any, 0, len(report.Debts)),
	}
	for _, debt := range report.Debts {
		balance := any(nil)
		if debt.BalanceCents != nil {
			balance = cli.Money(*debt.BalanceCents)
		}
		limit := any(nil)
		if debt.LimitCents != nil {
			limit = cli.Money(*debt.LimitCents)
		}
		utilization := any(nil)
		if debt.BalanceCents != nil && debt.LimitCents != nil {
			if value := cli.Ratio(*debt.BalanceCents, *debt.LimitCents, 4); value != nil {
				utilization = *value
			}
		}
		apr := any(nil)
		if debt.APRBasisPoints != nil {
			apr = cli.ScaledInteger(*debt.APRBasisPoints, 4)
		}
		dueDay := any(nil)
		if debt.DueDay != nil {
			dueDay = *debt.DueDay
		}
		table.Rows = append(table.Rows, []any{
			debt.Name, debt.Type, balance, limit, utilization, apr, dueDay,
		})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "count", Value: report.Count},
			{Key: "total_debt", Value: cli.Money(report.TotalDebtCents)},
			{Key: "missing_balance", Value: report.MissingBalance},
		}},
		{Key: "debts", Value: table},
		{Key: "hint", Value: debtsHint(report)},
	}
}

func debtsHint(report store.DebtReport) string {
	if report.Count == 0 {
		return "no credit-card or loan accounts yet; run moneta sync"
	}
	if report.MissingBalance > 0 {
		return "run moneta sync to pull balances for debt accounts with no snapshot"
	}
	return "run moneta networth to compare total debt with assets"
}

func buildNetworthDocument(report store.NetworthReport, filter store.NetworthFilter) toon.Object {
	asOf := any(nil)
	if report.AsOf != "" {
		asOf = report.AsOf
	}
	byType := toon.Table{
		Fields: []string{"type", "count", "balance"},
		Rows:   make([][]any, 0, len(report.ByType)),
	}
	for _, group := range report.ByType {
		balance := any(nil)
		if group.BalancedCount > 0 {
			balance = cli.Money(group.BalanceCents)
		}
		byType.Rows = append(byType.Rows, []any{group.Type, group.Count, balance})
	}
	return toon.Object{
		{Key: "summary", Value: toon.Object{
			{Key: "as_of", Value: asOf},
			{Key: "assets", Value: cli.Money(report.AssetsCents)},
			{Key: "liabilities", Value: cli.Money(report.LiabilitiesCents)},
			{Key: "networth", Value: cli.Money(report.NetworthCents)},
			{Key: "accounts", Value: report.Accounts},
			{Key: "missing_balance", Value: report.MissingBalance},
		}},
		{Key: "by_type", Value: byType},
		{Key: "hint", Value: networthHint(report, filter)},
	}
}

func networthHint(report store.NetworthReport, filter store.NetworthFilter) string {
	if report.Accounts == 0 {
		return "no accounts yet; run moneta link to connect an institution, then moneta sync"
	}
	if report.MissingBalance == report.Accounts && filter.AsOf != "" {
		return fmt.Sprintf(
			"no balance snapshots on or before %s; choose a later as_of date or run moneta sync",
			filter.AsOf,
		)
	}
	if report.MissingBalance > 0 {
		return "run moneta sync to pull balances for accounts without an eligible snapshot"
	}
	return "run moneta accounts to inspect account-level balances"
}

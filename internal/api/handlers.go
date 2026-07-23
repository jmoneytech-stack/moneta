package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func (s *server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "limit", "full"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	limit, full, err := parseLimit(query)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	items, err := store.ListProviderItemStatuses(request.Context(), s.db)
	if err != nil {
		s.internalError(writer, "read status", err)
		return
	}
	writeDocument(writer, buildStatusDocument(items, limit, full))
}

func (s *server) handleAccounts(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "type", "limit", "full"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	accountType, err := queryValue(query, "type")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if accountType != "" && !validAccountType(accountType) {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("unknown account type %q", accountType))
		return
	}
	limit, full, err := parseLimit(query)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	accounts, err := store.ListAccountSummaries(request.Context(), s.db, accountType)
	if err != nil {
		s.internalError(writer, "read accounts", err)
		return
	}
	writeDocument(writer, buildAccountsDocument(accounts, accountType, limit, full))
}

func validAccountType(accountType string) bool {
	switch canon.AccountType(accountType) {
	case canon.AccountTypeChecking,
		canon.AccountTypeSavings,
		canon.AccountTypeCreditCard,
		canon.AccountTypeLoan,
		canon.AccountTypeInvestment,
		canon.AccountTypeAsset:
		return true
	default:
		return false
	}
}

func (s *server) handleTransactions(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "from", "to", "account", "search", "limit", "full"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	filter, err := transactionFilter(query)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	limit, _, err := parseLimit(query)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	summary, err := store.SummarizeTransactions(request.Context(), s.db, filter)
	if err != nil {
		s.internalError(writer, "summarize transactions", err)
		return
	}
	transactions, err := store.ListTransactions(request.Context(), s.db, filter, limit)
	if err != nil {
		s.internalError(writer, "read transactions", err)
		return
	}
	writeDocument(writer, buildTransactionsDocument(summary, transactions, filter))
}

func transactionFilter(query map[string][]string) (store.TransactionFilter, error) {
	from, err := queryValue(query, "from")
	if err != nil {
		return store.TransactionFilter{}, err
	}
	to, err := queryValue(query, "to")
	if err != nil {
		return store.TransactionFilter{}, err
	}
	account, err := queryValue(query, "account")
	if err != nil {
		return store.TransactionFilter{}, err
	}
	search, err := queryValue(query, "search")
	if err != nil {
		return store.TransactionFilter{}, err
	}
	if err := validateDate("from", from); err != nil {
		return store.TransactionFilter{}, err
	}
	if err := validateDate("to", to); err != nil {
		return store.TransactionFilter{}, err
	}
	if from != "" && to != "" && from > to {
		return store.TransactionFilter{}, fmt.Errorf("query parameter %q must not be after %q", "from", "to")
	}
	return store.TransactionFilter{From: from, To: to, Account: account, Search: search}, nil
}

func (s *server) handleSpend(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "period", "from", "to", "account", "limit", "full"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	selectedPeriod, err := resolvePeriod(query, time.Now())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	account, err := queryValue(query, "account")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	limit, _, err := parseLimit(query)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	filter := store.SpendFilter{From: selectedPeriod.from, To: selectedPeriod.to, Account: account}
	report, err := store.ReadSpend(request.Context(), s.db, filter, limit)
	if err != nil {
		s.internalError(writer, "read spend", err)
		return
	}
	writeDocument(writer, buildSpendDocument(report, filter))
}

func (s *server) handleCashflow(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "period", "from", "to", "account"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	selectedPeriod, err := resolvePeriod(query, time.Now())
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	account, err := queryValue(query, "account")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	filter := store.CashflowFilter{From: selectedPeriod.from, To: selectedPeriod.to, Account: account}
	summary, err := store.ReadCashflow(request.Context(), s.db, filter)
	if err != nil {
		s.internalError(writer, "read cashflow", err)
		return
	}
	writeDocument(writer, buildCashflowDocument(summary, filter))
}

func (s *server) handleNetworth(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(query, "as_of", "history"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	asOf, err := queryValue(query, "as_of")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	history, err := queryValue(query, "history")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	_, historyProvided := query["history"]
	if asOf != "" && historyProvided {
		writeError(
			writer,
			http.StatusBadRequest,
			"query parameter \"history\" cannot be combined with \"as_of\"",
		)
		return
	}
	if err := validateDate("as_of", asOf); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if historyProvided {
		now := time.Now()
		if s.now != nil {
			now = s.now()
		}
		filter, err := store.ResolveNetworthHistoryWindow(history, now)
		if err != nil {
			writeError(
				writer,
				http.StatusBadRequest,
				fmt.Sprintf("query parameter %q %v", "history", err),
			)
			return
		}
		report, err := store.ReadNetworthHistory(request.Context(), s.db, filter)
		if err != nil {
			s.internalError(writer, "read networth history", err)
			return
		}
		writeDocument(writer, buildNetworthHistoryDocument(report))
		return
	}

	filter := store.NetworthFilter{AsOf: asOf}
	report, err := store.ReadNetworth(request.Context(), s.db, filter)
	if err != nil {
		s.internalError(writer, "read networth", err)
		return
	}
	writeDocument(writer, buildNetworthDocument(report, filter))
}

func (s *server) handleTrends(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	if err := validateQueryKeys(
		query,
		"metric",
		"history",
		"period",
		"from",
		"to",
		"account",
		"limit",
		"full",
	); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	metric, err := queryValue(query, "metric")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if metric == "" {
		writeError(
			writer,
			http.StatusBadRequest,
			"query parameter \"metric\" is required (supported: mom, merchants, utilization)",
		)
		return
	}
	if metric != "mom" && metric != "merchants" && metric != "utilization" {
		writeError(
			writer,
			http.StatusBadRequest,
			fmt.Sprintf("unknown metric %q (supported: mom, merchants, utilization)", metric),
		)
		return
	}

	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	_, historyProvided := query["history"]
	var momPeriod store.TrendMoMPeriod
	var merchantsPeriod period
	var utilizationPeriod period
	switch metric {
	case "mom":
		if historyProvided {
			writeError(writer, http.StatusBadRequest, "history is supported only by metric utilization")
			return
		}
		from, err := queryValue(query, "from")
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		to, err := queryValue(query, "to")
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		if from != "" || to != "" {
			writeError(
				writer,
				http.StatusBadRequest,
				"metric mom requires period=YYYY-MM or the default current month; from/to are unsupported",
			)
			return
		}
		periodValue, err := queryValue(query, "period")
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		momPeriod, err = store.ResolveTrendMoMPeriod(periodValue, now)
		if err != nil {
			writeError(
				writer,
				http.StatusBadRequest,
				fmt.Sprintf("query parameter %q %v", "period", err),
			)
			return
		}
	case "merchants":
		if historyProvided {
			writeError(writer, http.StatusBadRequest, "history is supported only by metric utilization")
			return
		}
		merchantsPeriod, err = resolvePeriod(query, now)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
	case "utilization":
		if _, ok := query["limit"]; ok {
			writeError(writer, http.StatusBadRequest, "limit/full are unsupported by metric utilization")
			return
		}
		if _, ok := query["full"]; ok {
			writeError(writer, http.StatusBadRequest, "limit/full are unsupported by metric utilization")
			return
		}
		_, periodProvided := query["period"]
		_, fromProvided := query["from"]
		_, toProvided := query["to"]
		if historyProvided && (periodProvided || fromProvided || toProvided) {
			writeError(
				writer,
				http.StatusBadRequest,
				"query parameter \"history\" cannot be combined with \"period\", \"from\", or \"to\"",
			)
			return
		}
		if historyProvided {
			history, err := queryValue(query, "history")
			if err != nil {
				writeError(writer, http.StatusBadRequest, err.Error())
				return
			}
			window, err := store.ResolveNetworthHistoryWindow(history, now)
			if err != nil {
				writeError(
					writer,
					http.StatusBadRequest,
					fmt.Sprintf("query parameter %q %v", "history", err),
				)
				return
			}
			utilizationPeriod = period{from: window.From, to: window.To}
		} else if periodProvided || fromProvided || toProvided {
			utilizationPeriod, err = resolvePeriod(query, now)
			if err != nil {
				writeError(writer, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			window, err := store.ResolveNetworthHistoryWindow("30d", now)
			if err != nil {
				s.internalError(writer, "resolve default utilization window", err)
				return
			}
			utilizationPeriod = period{from: window.From, to: window.To}
		}
	}

	account, err := queryValue(query, "account")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	limit := 0
	if metric != "utilization" {
		limit, _, err = parseLimit(query)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
	}

	switch metric {
	case "mom":
		filter := store.TrendMoMFilter{
			ThisFrom: momPeriod.ThisFrom,
			ThisTo:   momPeriod.ThisTo,
			PrevFrom: momPeriod.PrevFrom,
			PrevTo:   momPeriod.PrevTo,
			Account:  account,
		}
		report, err := store.ReadTrendMoM(request.Context(), s.db, filter, limit)
		if err != nil {
			s.internalError(writer, "read month-over-month trends", err)
			return
		}
		writeDocument(writer, buildTrendMoMDocument(report, filter))
	case "merchants":
		filter := store.TrendMerchantsFilter{
			From: merchantsPeriod.from, To: merchantsPeriod.to, Account: account,
		}
		report, err := store.ReadTrendMerchants(request.Context(), s.db, filter, limit)
		if err != nil {
			s.internalError(writer, "read merchant trends", err)
			return
		}
		writeDocument(writer, buildTrendMerchantsDocument(report, filter))
	case "utilization":
		filter := store.TrendUtilizationFilter{
			From: utilizationPeriod.from, To: utilizationPeriod.to, Account: account,
		}
		report, err := store.ReadTrendUtilization(request.Context(), s.db, filter)
		if err != nil {
			s.internalError(writer, "read utilization trends", err)
			return
		}
		writeDocument(writer, buildTrendUtilizationDocument(report))
	}
}

func (s *server) handleDebts(writer http.ResponseWriter, request *http.Request) {
	if err := validateQueryKeys(request.URL.Query()); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	report, err := store.ReadDebts(request.Context(), s.db)
	if err != nil {
		s.internalError(writer, "read debts", err)
		return
	}
	writeDocument(writer, buildDebtsDocument(report))
}

func (s *server) internalError(writer http.ResponseWriter, operation string, err error) {
	s.logger.Printf("REST %s: %v", operation, err)
	writeError(writer, http.StatusInternalServerError, "internal server error")
}

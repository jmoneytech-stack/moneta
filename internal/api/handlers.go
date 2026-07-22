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
	if err := validateQueryKeys(query, "as_of"); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	asOf, err := queryValue(query, "as_of")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateDate("as_of", asOf); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
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

func (s *server) internalError(writer http.ResponseWriter, operation string, err error) {
	s.logger.Printf("REST %s: %v", operation, err)
	writeError(writer, http.StatusInternalServerError, "internal server error")
}

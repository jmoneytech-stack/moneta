package plaid

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

func normalizeTransactions(transactions []rawTransaction) ([]canon.Transaction, error) {
	canonicalTransactions := make([]canon.Transaction, 0, len(transactions))
	for _, transaction := range transactions {
		if transaction.ID == "" || transaction.AccountID == "" {
			return nil, fmt.Errorf("Plaid transaction and account ids are required")
		}
		amount, err := transactionAmountToCents(transaction.Amount)
		if err != nil {
			return nil, fmt.Errorf("transaction %q amount: %w", transaction.ID, err)
		}
		currency, err := canonicalPlaidCurrency(
			transaction.Currency,
			transaction.UnofficialCurrency,
		)
		if err != nil {
			return nil, fmt.Errorf("transaction %q: %w", transaction.ID, err)
		}
		merchant := transaction.OriginalDescription
		if merchant == "" {
			merchant = transaction.Name
		}
		status := canon.TxnStatusPosted
		if transaction.Pending {
			status = canon.TxnStatusPending
		}
		canonicalTransactions = append(canonicalTransactions, canon.Transaction{
			ProviderTxnID:  transaction.ID,
			PendingTxnID:   transaction.PendingID,
			AccountRef:     transaction.AccountID,
			Date:           canon.Date(transaction.Date),
			AmountCents:    amount,
			MerchantRaw:    merchant,
			SourceCategory: transaction.Category,
			Status:         status,
			Currency:       currency,
		})
	}
	return canonicalTransactions, nil
}

func normalizeLiabilities(
	liabilities rawLiabilities,
	accounts map[string]rawAccount,
) ([]canon.Liability, error) {
	order := make([]string, 0)
	byAccount := make(map[string]canon.Liability)
	merge := func(liability canon.Liability) {
		existing, found := byAccount[liability.AccountRef]
		if !found {
			order = append(order, liability.AccountRef)
			byAccount[liability.AccountRef] = liability
			return
		}
		if liability.APR > existing.APR {
			existing.APR = liability.APR
		}
		if liability.LimitCents > existing.LimitCents {
			existing.LimitCents = liability.LimitCents
		}
		if liability.MinPaymentCents > existing.MinPaymentCents {
			existing.MinPaymentCents = liability.MinPaymentCents
		}
		if liability.LastStatementCents > existing.LastStatementCents {
			existing.LastStatementCents = liability.LastStatementCents
		}
		if existing.StatementDay == 0 {
			existing.StatementDay = liability.StatementDay
		}
		if existing.DueDay == 0 || liability.DueDay != 0 && liability.DueDay < existing.DueDay {
			existing.DueDay = liability.DueDay
		}
		byAccount[liability.AccountRef] = existing
	}

	for _, credit := range liabilities.Credit {
		limit, err := optionalMoneyToCents(accounts[credit.AccountID].Limit)
		if err != nil {
			return nil, fmt.Errorf("credit liability %q limit: %w", credit.AccountID, err)
		}
		minimum, err := optionalMoneyToCents(credit.MinimumPayment)
		if err != nil {
			return nil, fmt.Errorf("credit liability %q minimum payment: %w", credit.AccountID, err)
		}
		statement, err := optionalMoneyToCents(credit.LastStatementBalance)
		if err != nil {
			return nil, fmt.Errorf("credit liability %q statement balance: %w", credit.AccountID, err)
		}
		statementDay, err := dateDay(credit.LastStatementIssueDate)
		if err != nil {
			return nil, fmt.Errorf("credit liability %q statement date: %w", credit.AccountID, err)
		}
		dueDay, err := dateDay(credit.NextPaymentDueDate)
		if err != nil {
			return nil, fmt.Errorf("credit liability %q due date: %w", credit.AccountID, err)
		}
		merge(canon.Liability{
			AccountRef:         credit.AccountID,
			APR:                preferredAPR(credit.APRs),
			LimitCents:         limit,
			MinPaymentCents:    minimum,
			LastStatementCents: statement,
			StatementDay:       statementDay,
			DueDay:             dueDay,
		})
	}
	for _, student := range liabilities.Student {
		minimum, err := optionalMoneyToCents(student.MinimumPayment)
		if err != nil {
			return nil, fmt.Errorf("student liability %q minimum payment: %w", student.AccountID, err)
		}
		statement, err := optionalMoneyToCents(student.LastStatementBalance)
		if err != nil {
			return nil, fmt.Errorf("student liability %q statement balance: %w", student.AccountID, err)
		}
		statementDay, err := dateDay(student.LastStatementIssueDate)
		if err != nil {
			return nil, fmt.Errorf("student liability %q statement date: %w", student.AccountID, err)
		}
		dueDay, err := dateDay(student.NextPaymentDueDate)
		if err != nil {
			return nil, fmt.Errorf("student liability %q due date: %w", student.AccountID, err)
		}
		merge(canon.Liability{
			AccountRef:         student.AccountID,
			APR:                student.InterestRatePercentage,
			MinPaymentCents:    minimum,
			LastStatementCents: statement,
			StatementDay:       statementDay,
			DueDay:             dueDay,
		})
	}
	for _, mortgage := range liabilities.Mortgage {
		minimum, err := optionalMoneyToCents(mortgage.NextMonthlyPayment)
		if err != nil {
			return nil, fmt.Errorf("mortgage liability %q monthly payment: %w", mortgage.AccountID, err)
		}
		dueDay, err := dateDay(mortgage.NextPaymentDueDate)
		if err != nil {
			return nil, fmt.Errorf("mortgage liability %q due date: %w", mortgage.AccountID, err)
		}
		apr := float64(0)
		if mortgage.InterestPercentage != nil {
			apr = *mortgage.InterestPercentage
		}
		merge(canon.Liability{
			AccountRef:      mortgage.AccountID,
			APR:             apr,
			MinPaymentCents: minimum,
			DueDay:          dueDay,
		})
	}

	canonicalLiabilities := make([]canon.Liability, 0, len(order))
	for _, accountID := range order {
		liability := byAccount[accountID]
		if liability.AccountRef == "" {
			return nil, fmt.Errorf("Plaid liability account id is empty")
		}
		if math.IsNaN(liability.APR) || math.IsInf(liability.APR, 0) {
			return nil, fmt.Errorf("Plaid liability %q APR is not finite", accountID)
		}
		canonicalLiabilities = append(canonicalLiabilities, liability)
	}
	return canonicalLiabilities, nil
}

func canonicalAccountType(accountType, subtype string) (canon.AccountType, error) {
	switch accountType {
	case "depository":
		switch subtype {
		case "savings", "money market", "cd", "cash isa":
			return canon.AccountTypeSavings, nil
		default:
			return canon.AccountTypeChecking, nil
		}
	case "credit":
		return canon.AccountTypeCreditCard, nil
	case "loan":
		return canon.AccountTypeLoan, nil
	case "investment", "brokerage":
		return canon.AccountTypeInvestment, nil
	case "other":
		return canon.AccountTypeAsset, nil
	default:
		return "", fmt.Errorf("unsupported Plaid account type %q", accountType)
	}
}

func canonicalPlaidCurrency(isoCurrency, unofficialCurrency string) (string, error) {
	if unofficialCurrency != "" {
		return "", fmt.Errorf("unofficial currency is unsupported")
	}
	if isoCurrency == "" {
		return "USD", nil
	}
	isoCurrency = strings.ToUpper(isoCurrency)
	if isoCurrency != "USD" {
		return "", fmt.Errorf("currency %q is unsupported", isoCurrency)
	}
	return isoCurrency, nil
}

func optionalMoneyToCents(amount *float64) (int64, error) {
	if amount == nil {
		return 0, nil
	}
	return moneyToCents(*amount)
}

func preferredAPR(aprs []rawAPR) float64 {
	if len(aprs) == 0 {
		return 0
	}
	for _, apr := range aprs {
		if strings.Contains(strings.ToLower(apr.Type), "purchase") {
			return apr.Percentage
		}
	}
	return aprs[0].Percentage
}

func dateDay(date string) (int, error) {
	if date == "" {
		return 0, nil
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date {
		return 0, fmt.Errorf("date %q must use valid YYYY-MM-DD form", date)
	}
	return parsed.Day(), nil
}

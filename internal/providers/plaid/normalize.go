package plaid

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

// normalizeTransactions converts raw Plaid transactions one row at a time.
// Rows that cannot be normalized are skipped and recorded instead of failing
// the batch, so a single poison record cannot wedge the Item cursor.
func normalizeTransactions(
	transactions []rawTransaction,
) ([]canon.Transaction, []canon.SkippedRecord) {
	canonicalTransactions := make([]canon.Transaction, 0, len(transactions))
	var skipped []canon.SkippedRecord
	for _, transaction := range transactions {
		if transaction.ID == "" || transaction.AccountID == "" {
			skipped = append(skipped, canon.SkippedRecord{
				Kind:   canon.RecordKindTransaction,
				ID:     transaction.ID,
				Reason: canon.SkipMalformedRecord,
				Detail: "missing transaction or account id",
			})
			continue
		}
		amount, err := transactionAmountToCents(transaction.Amount)
		if err != nil {
			skipped = append(skipped, canon.SkippedRecord{
				Kind:   canon.RecordKindTransaction,
				ID:     transaction.ID,
				Reason: canon.SkipMalformedRecord,
				Detail: "invalid amount",
			})
			continue
		}
		currency, err := canonicalPlaidCurrency(
			transaction.Currency,
			transaction.UnofficialCurrency,
		)
		if err != nil {
			skipped = append(skipped, canon.SkippedRecord{
				Kind:   canon.RecordKindTransaction,
				ID:     transaction.ID,
				Reason: canon.SkipUnsupportedCurrency,
				Detail: currencySkipDetail(
					transaction.Currency,
					transaction.UnofficialCurrency,
				),
			})
			continue
		}
		if !validISODate(transaction.Date) {
			skipped = append(skipped, canon.SkippedRecord{
				Kind:   canon.RecordKindTransaction,
				ID:     transaction.ID,
				Reason: canon.SkipMalformedRecord,
				Detail: "invalid date",
			})
			continue
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
	return canonicalTransactions, skipped
}

// normalizeLiabilities converts raw Plaid liabilities one row at a time.
// Rows that cannot be normalized are skipped and recorded instead of failing
// the batch, so a single poison record cannot wedge the Item cursor.
func normalizeLiabilities(
	liabilities rawLiabilities,
	accounts map[string]rawAccount,
) ([]canon.Liability, []canon.SkippedRecord) {
	var skipped []canon.SkippedRecord
	skip := func(accountID, detail string) {
		skipped = append(skipped, canon.SkippedRecord{
			Kind:   canon.RecordKindLiability,
			ID:     accountID,
			Reason: canon.SkipMalformedRecord,
			Detail: detail,
		})
	}

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
			skip(credit.AccountID, "invalid limit")
			continue
		}
		minimum, err := optionalMoneyToCents(credit.MinimumPayment)
		if err != nil {
			skip(credit.AccountID, "invalid minimum payment")
			continue
		}
		statement, err := optionalMoneyToCents(credit.LastStatementBalance)
		if err != nil {
			skip(credit.AccountID, "invalid statement balance")
			continue
		}
		statementDay, err := dateDay(credit.LastStatementIssueDate)
		if err != nil {
			skip(credit.AccountID, "invalid statement date")
			continue
		}
		dueDay, err := dateDay(credit.NextPaymentDueDate)
		if err != nil {
			skip(credit.AccountID, "invalid due date")
			continue
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
			skip(student.AccountID, "invalid minimum payment")
			continue
		}
		statement, err := optionalMoneyToCents(student.LastStatementBalance)
		if err != nil {
			skip(student.AccountID, "invalid statement balance")
			continue
		}
		statementDay, err := dateDay(student.LastStatementIssueDate)
		if err != nil {
			skip(student.AccountID, "invalid statement date")
			continue
		}
		dueDay, err := dateDay(student.NextPaymentDueDate)
		if err != nil {
			skip(student.AccountID, "invalid due date")
			continue
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
			skip(mortgage.AccountID, "invalid monthly payment")
			continue
		}
		dueDay, err := dateDay(mortgage.NextPaymentDueDate)
		if err != nil {
			skip(mortgage.AccountID, "invalid due date")
			continue
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
			skip(accountID, "missing account id")
			continue
		}
		if math.IsNaN(liability.APR) || math.IsInf(liability.APR, 0) {
			skip(accountID, "invalid APR")
			continue
		}
		canonicalLiabilities = append(canonicalLiabilities, liability)
	}

	// An account malformed in one liability array (e.g. credit) but valid in
	// another (e.g. student) merges one valid liability; drop its earlier
	// skip so SyncResult.Skipped does not over-count it.
	recovered := make(map[string]bool, len(canonicalLiabilities))
	for _, liability := range canonicalLiabilities {
		recovered[liability.AccountRef] = true
	}
	keptSkipped := skipped[:0]
	for _, record := range skipped {
		if recovered[record.ID] {
			continue
		}
		keptSkipped = append(keptSkipped, record)
	}
	return canonicalLiabilities, keptSkipped
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

// currencySkipDetail reports the offending currency code for a skipped
// record. Codes are static provider vocabulary, never personal data.
func currencySkipDetail(isoCurrency, unofficialCurrency string) string {
	if unofficialCurrency != "" {
		return strings.ToUpper(unofficialCurrency)
	}
	return strings.ToUpper(isoCurrency)
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

// validISODate reports whether date is a real calendar date in strict
// YYYY-MM-DD form.
func validISODate(date string) bool {
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

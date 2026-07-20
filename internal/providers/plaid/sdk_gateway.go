package plaid

import (
	"context"

	plaidSDK "github.com/plaid/plaid-go/v43/plaid"
)

const syncPageSize int32 = 500

const maxTransactionHistoryDays int32 = 730

type sdkGateway struct {
	client *plaidSDK.APIClient
}

func (g *sdkGateway) createLinkToken(
	ctx context.Context,
	clientUserID string,
) (string, error) {
	request := plaidSDK.NewLinkTokenCreateRequest(
		"Moneta",
		"en",
		[]plaidSDK.CountryCode{plaidSDK.COUNTRYCODE_US},
	)
	request.SetUser(*plaidSDK.NewLinkTokenCreateRequestUser(clientUserID))
	request.SetProducts([]plaidSDK.Products{plaidSDK.PRODUCTS_TRANSACTIONS})
	request.SetRequiredIfSupportedProducts(
		[]plaidSDK.Products{plaidSDK.PRODUCTS_LIABILITIES},
	)
	transactions := plaidSDK.NewLinkTokenTransactions()
	transactions.SetDaysRequested(maxTransactionHistoryDays)
	request.SetTransactions(*transactions)

	response, _, err := g.client.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*request).
		Execute()
	if err != nil {
		return "", sanitizeSDKError(ctx, err)
	}
	return response.GetLinkToken(), nil
}

func (g *sdkGateway) exchangePublicToken(
	ctx context.Context,
	publicToken string,
) (rawLinkExchange, error) {
	request := plaidSDK.NewItemPublicTokenExchangeRequest(publicToken)
	response, _, err := g.client.PlaidApi.ItemPublicTokenExchange(ctx).
		ItemPublicTokenExchangeRequest(*request).
		Execute()
	if err != nil {
		return rawLinkExchange{}, sanitizeSDKError(ctx, err)
	}
	return rawLinkExchange{
		ItemID:      response.GetItemId(),
		AccessToken: response.GetAccessToken(),
	}, nil
}

func (g *sdkGateway) transactionsSync(
	ctx context.Context,
	accessToken string,
	cursor string,
) (rawSyncPage, error) {
	request := plaidSDK.NewTransactionsSyncRequest(accessToken)
	request.SetCount(syncPageSize)
	if cursor != "" {
		request.SetCursor(cursor)
	}
	options := plaidSDK.NewTransactionsSyncRequestOptionsWithDefaults()
	options.SetIncludeOriginalDescription(true)
	options.SetPersonalFinanceCategoryVersion(plaidSDK.PERSONALFINANCECATEGORYVERSION_V2)
	request.SetOptions(*options)

	response, _, err := g.client.PlaidApi.TransactionsSync(ctx).
		TransactionsSyncRequest(*request).
		Execute()
	if err != nil {
		return rawSyncPage{}, sanitizeSDKError(ctx, err)
	}

	page := rawSyncPage{
		Accounts:   make([]rawAccount, 0, len(response.GetAccounts())),
		Added:      make([]rawTransaction, 0, len(response.GetAdded())),
		Modified:   make([]rawTransaction, 0, len(response.GetModified())),
		Removed:    make([]string, 0, len(response.GetRemoved())),
		NextCursor: response.GetNextCursor(),
		HasMore:    response.GetHasMore(),
	}
	for index := range response.Accounts {
		page.Accounts = append(page.Accounts, accountFromSDK(&response.Accounts[index]))
	}
	for index := range response.Added {
		page.Added = append(page.Added, transactionFromSDK(&response.Added[index]))
	}
	for index := range response.Modified {
		page.Modified = append(page.Modified, transactionFromSDK(&response.Modified[index]))
	}
	for index := range response.Removed {
		page.Removed = append(page.Removed, response.Removed[index].GetTransactionId())
	}
	return page, nil
}

func (g *sdkGateway) accounts(ctx context.Context, accessToken string) ([]rawAccount, error) {
	request := plaidSDK.NewAccountsGetRequest(accessToken)
	response, _, err := g.client.PlaidApi.AccountsGet(ctx).
		AccountsGetRequest(*request).
		Execute()
	if err != nil {
		return nil, sanitizeSDKError(ctx, err)
	}

	accounts := make([]rawAccount, 0, len(response.GetAccounts()))
	for index := range response.Accounts {
		accounts = append(accounts, accountFromSDK(&response.Accounts[index]))
	}
	return accounts, nil
}

func (g *sdkGateway) liabilities(ctx context.Context, accessToken string) (rawLiabilities, error) {
	request := plaidSDK.NewLiabilitiesGetRequest(accessToken)
	response, _, err := g.client.PlaidApi.LiabilitiesGet(ctx).
		LiabilitiesGetRequest(*request).
		Execute()
	if err != nil {
		return rawLiabilities{}, sanitizeSDKError(ctx, err)
	}

	result := rawLiabilities{
		Accounts: make([]rawAccount, 0, len(response.GetAccounts())),
	}
	for index := range response.Accounts {
		result.Accounts = append(result.Accounts, accountFromSDK(&response.Accounts[index]))
	}
	liabilities := response.GetLiabilities()
	for index := range liabilities.Credit {
		liability := &liabilities.Credit[index]
		credit := rawCreditLiability{
			AccountID:              liability.GetAccountId(),
			LastStatementBalance:   copyFloat(liability.GetLastStatementBalanceOk()),
			MinimumPayment:         copyFloat(liability.GetMinimumPaymentAmountOk()),
			LastStatementIssueDate: liability.GetLastStatementIssueDate(),
			NextPaymentDueDate:     liability.GetNextPaymentDueDate(),
			APRs:                   make([]rawAPR, 0, len(liability.GetAprs())),
		}
		for aprIndex := range liability.Aprs {
			credit.APRs = append(credit.APRs, rawAPR{
				Percentage: liability.Aprs[aprIndex].GetAprPercentage(),
				Type:       liability.Aprs[aprIndex].GetAprType(),
			})
		}
		result.Credit = append(result.Credit, credit)
	}
	for index := range liabilities.Student {
		liability := &liabilities.Student[index]
		result.Student = append(result.Student, rawStudentLiability{
			AccountID:              liability.GetAccountId(),
			InterestRatePercentage: liability.GetInterestRatePercentage(),
			LastStatementBalance:   copyFloat(liability.GetLastStatementBalanceOk()),
			MinimumPayment:         copyFloat(liability.GetMinimumPaymentAmountOk()),
			LastStatementIssueDate: liability.GetLastStatementIssueDate(),
			NextPaymentDueDate:     liability.GetNextPaymentDueDate(),
		})
	}
	for index := range liabilities.Mortgage {
		liability := &liabilities.Mortgage[index]
		result.Mortgage = append(result.Mortgage, rawMortgageLiability{
			AccountID:          liability.GetAccountId(),
			InterestPercentage: copyFloat(liability.InterestRate.GetPercentageOk()),
			NextMonthlyPayment: copyFloat(liability.GetNextMonthlyPaymentOk()),
			NextPaymentDueDate: liability.GetNextPaymentDueDate(),
		})
	}
	return result, nil
}

func (g *sdkGateway) item(ctx context.Context, accessToken string) (rawItem, error) {
	request := plaidSDK.NewItemGetRequest(accessToken)
	response, _, err := g.client.PlaidApi.ItemGet(ctx).
		ItemGetRequest(*request).
		Execute()
	if err != nil {
		return rawItem{}, sanitizeSDKError(ctx, err)
	}

	item := response.GetItem()
	result := rawItem{
		ID:          item.GetItemId(),
		Institution: item.GetInstitutionName(),
	}
	if itemError, ok := item.GetErrorOk(); ok && itemError != nil {
		result.ErrorCode = itemError.GetErrorCode()
	}
	return result, nil
}

func accountFromSDK(account *plaidSDK.AccountBase) rawAccount {
	balances := account.GetBalances()
	return rawAccount{
		ID:                 account.GetAccountId(),
		Name:               account.GetName(),
		OfficialName:       account.GetOfficialName(),
		Mask:               account.GetMask(),
		Type:               string(account.GetType()),
		Subtype:            string(account.GetSubtype()),
		Currency:           balances.GetIsoCurrencyCode(),
		UnofficialCurrency: balances.GetUnofficialCurrencyCode(),
		Current:            copyFloat(balances.GetCurrentOk()),
		Available:          copyFloat(balances.GetAvailableOk()),
		Limit:              copyFloat(balances.GetLimitOk()),
	}
}

func transactionFromSDK(transaction *plaidSDK.Transaction) rawTransaction {
	category := ""
	if personalCategory, ok := transaction.GetPersonalFinanceCategoryOk(); ok && personalCategory != nil {
		category = personalCategory.GetPrimary()
	}
	return rawTransaction{
		ID:                  transaction.GetTransactionId(),
		PendingID:           transaction.GetPendingTransactionId(),
		AccountID:           transaction.GetAccountId(),
		Date:                transaction.GetDate(),
		Amount:              transaction.GetAmount(),
		Name:                transaction.GetName(),
		OriginalDescription: transaction.GetOriginalDescription(),
		Category:            category,
		Pending:             transaction.GetPending(),
		Currency:            transaction.GetIsoCurrencyCode(),
		UnofficialCurrency:  transaction.GetUnofficialCurrencyCode(),
	}
}

func copyFloat(value *float64, ok bool) *float64 {
	if !ok || value == nil {
		return nil
	}
	copy := *value
	return &copy
}

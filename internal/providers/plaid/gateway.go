package plaid

import "context"

type gateway interface {
	transactionsSync(ctx context.Context, accessToken, cursor string) (rawSyncPage, error)
	accounts(ctx context.Context, accessToken string) ([]rawAccount, error)
	liabilities(ctx context.Context, accessToken string) (rawLiabilities, error)
	item(ctx context.Context, accessToken string) (rawItem, error)
}

type linkGateway interface {
	createLinkToken(ctx context.Context, clientUserID string) (string, error)
	exchangePublicToken(ctx context.Context, publicToken string) (rawLinkExchange, error)
}

type rawLinkExchange struct {
	ItemID      string
	AccessToken string
}

type rawSyncPage struct {
	Accounts   []rawAccount
	Added      []rawTransaction
	Modified   []rawTransaction
	Removed    []string
	NextCursor string
	HasMore    bool
}

type rawAccount struct {
	ID                 string
	Name               string
	OfficialName       string
	Mask               string
	Type               string
	Subtype            string
	Currency           string
	UnofficialCurrency string
	Current            *float64
	Available          *float64
	Limit              *float64
}

type rawTransaction struct {
	ID                  string
	PendingID           string
	AccountID           string
	Date                string
	Amount              float64
	Name                string
	OriginalDescription string
	Category            string
	Pending             bool
	Currency            string
	UnofficialCurrency  string
}

type rawLiabilities struct {
	Accounts []rawAccount
	Credit   []rawCreditLiability
	Student  []rawStudentLiability
	Mortgage []rawMortgageLiability
}

type rawAPR struct {
	Percentage float64
	Type       string
}

type rawCreditLiability struct {
	AccountID              string
	APRs                   []rawAPR
	LastStatementBalance   *float64
	MinimumPayment         *float64
	LastStatementIssueDate string
	NextPaymentDueDate     string
}

type rawStudentLiability struct {
	AccountID              string
	InterestRatePercentage float64
	LastStatementBalance   *float64
	MinimumPayment         *float64
	LastStatementIssueDate string
	NextPaymentDueDate     string
}

type rawMortgageLiability struct {
	AccountID          string
	InterestPercentage *float64
	NextMonthlyPayment *float64
	NextPaymentDueDate string
}

type rawItem struct {
	ID          string
	Institution string
	ErrorCode   string
}

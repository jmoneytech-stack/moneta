package canon

// AccountType identifies the canonical purpose of an account.
type AccountType string

const (
	AccountTypeChecking   AccountType = "checking"
	AccountTypeSavings    AccountType = "savings"
	AccountTypeCreditCard AccountType = "credit_card"
	AccountTypeLoan       AccountType = "loan"
	AccountTypeInvestment AccountType = "investment"
	AccountTypeAsset      AccountType = "asset"
)

// TxnStatus is the lifecycle state of a transaction.
type TxnStatus string

const (
	TxnStatusPending TxnStatus = "pending"
	TxnStatusPosted  TxnStatus = "posted"
)

// Date is an ISO 8601 calendar date in YYYY-MM-DD form.
type Date string

// Account is an account as reported by a provider.
type Account struct {
	ProviderAccountID string
	Name              string
	Institution       string
	Mask              string
	Type              AccountType
	Currency          string
}

// Transaction is a provider transaction normalized to Moneta's sign
// convention. AmountCents is always integer cents, with negative values
// representing outflows.
type Transaction struct {
	ProviderTxnID  string
	PendingTxnID   string
	AccountRef     string
	Date           Date
	AmountCents    int64
	MerchantRaw    string
	SourceCategory string
	Status         TxnStatus
	Currency       string
}

// Balance is an account balance observed on a given date. Every monetary value
// is represented in integer cents.
type Balance struct {
	AccountRef     string
	Date           Date
	CurrentCents   int64
	AvailableCents int64
	LimitCents     int64
}

// Liability contains provider-supplied credit or loan terms. APR is a rate,
// not a monetary value. Every monetary value is represented in integer cents.
type Liability struct {
	AccountRef         string
	APR                float64
	LimitCents         int64
	MinPaymentCents    int64
	LastStatementCents int64
	StatementDay       int
	DueDay             int
}

// ConnectionStatus describes provider connection health without exposing
// credentials or tokens.
type ConnectionStatus struct {
	ID          string
	Institution string
	State       string
	Detail      string
}

// SyncBatch is one incremental provider response. Providers are stateless;
// callers persist NextCursor only after successfully applying the full batch.
type SyncBatch struct {
	Accounts    []Account
	Added       []Transaction
	Modified    []Transaction
	Removed     []string
	Balances    []Balance
	Liabilities []Liability
	NextCursor  string
}

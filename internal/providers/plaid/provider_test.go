package plaid

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
	"github.com/jmoneytech-stack/moneta/internal/core"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const fakeAccessToken = "access-sandbox-fake-token"

func TestProviderImplementsCanonicalIdentityAndCapabilities(t *testing.T) {
	provider := mustTestProvider(t, &fakeGateway{})

	if provider.Name() != "plaid" {
		t.Fatalf("Name() = %q, want plaid", provider.Name())
	}
	want := canon.CapAccounts |
		canon.CapTransactions |
		canon.CapBalances |
		canon.CapLiabilities
	if provider.Capabilities() != want {
		t.Fatalf("Capabilities() = %08b, want %08b", provider.Capabilities(), want)
	}
	if provider.Capabilities().Has(canon.CapWrite) {
		t.Fatal("Plaid provider unexpectedly advertises write capability")
	}
}

func TestProviderSyncPaginatesAndNormalizesCompleteBatch(t *testing.T) {
	currentChecking := 1234.56
	availableChecking := 1200.00
	currentCard := 100.05
	cardLimit := 1000.00
	minimumPayment := 1.005
	lastStatement := 4.35

	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"start-cursor": {
				Added: []rawTransaction{{
					ID:                  "pending-id",
					AccountID:           "checking-1",
					Date:                "2026-07-01",
					Amount:              4.35,
					Name:                "COFFEE SHOP 123",
					OriginalDescription: "Coffee Shop",
					Category:            "FOOD_AND_DRINK",
					Pending:             true,
					Currency:            "USD",
				}},
				NextCursor: "page-2",
				HasMore:    true,
			},
			"page-2": {
				Modified: []rawTransaction{{
					ID:        "income-id",
					AccountID: "checking-1",
					Date:      "2026-07-02",
					Amount:    -1.005,
					Name:      "Payroll",
					Category:  "INCOME",
					Currency:  "USD",
				}},
				Removed:    []string{"removed-id"},
				NextCursor: "final-cursor",
			},
		},
		accountsResult: []rawAccount{
			{
				ID:        "checking-1",
				Name:      "Test Checking",
				Mask:      "0000",
				Type:      "depository",
				Subtype:   "checking",
				Currency:  "USD",
				Current:   &currentChecking,
				Available: &availableChecking,
			},
			{
				ID:       "card-1",
				Name:     "Test Card",
				Mask:     "1111",
				Type:     "credit",
				Subtype:  "credit card",
				Currency: "USD",
				Current:  &currentCard,
				Limit:    &cardLimit,
			},
		},
		liabilitiesResult: rawLiabilities{
			Credit: []rawCreditLiability{{
				AccountID: "card-1",
				APRs: []rawAPR{
					{Percentage: 29.99, Type: "cash_apr"},
					{Percentage: 19.99, Type: "purchase_apr"},
				},
				MinimumPayment:         &minimumPayment,
				LastStatementBalance:   &lastStatement,
				LastStatementIssueDate: "2026-07-05",
				NextPaymentDueDate:     "2026-07-28",
			}},
		},
	}
	provider := mustTestProvider(t, gateway)
	provider.now = func() time.Time {
		return time.Date(2026, time.July, 17, 23, 0, 0, 0, time.FixedZone("test", 8*60*60))
	}

	batch, err := provider.Sync(context.Background(), "start-cursor")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if !reflect.DeepEqual(gateway.syncCursors, []string{"start-cursor", "page-2"}) {
		t.Fatalf("sync cursors = %v, want start-cursor then page-2", gateway.syncCursors)
	}
	if batch.NextCursor != "final-cursor" {
		t.Errorf("NextCursor = %q, want final-cursor", batch.NextCursor)
	}
	if len(batch.Accounts) != 2 || batch.Accounts[0].Type != canon.AccountTypeChecking ||
		batch.Accounts[1].Type != canon.AccountTypeCreditCard {
		t.Fatalf("normalized accounts = %#v", batch.Accounts)
	}
	if batch.Accounts[0].Institution != "Sandbox Bank" {
		t.Errorf("account institution = %q, want Sandbox Bank", batch.Accounts[0].Institution)
	}
	if len(batch.Balances) != 2 {
		t.Fatalf("balance count = %d, want 2", len(batch.Balances))
	}
	if batch.Balances[0].CurrentCents != 123456 ||
		batch.Balances[0].AvailableCents != 120000 {
		t.Errorf("checking balance = %#v", batch.Balances[0])
	}
	if batch.Balances[0].Date != "2026-07-17" {
		t.Errorf("balance date = %q, want 2026-07-17", batch.Balances[0].Date)
	}
	if batch.Balances[1].CurrentCents != 10005 || batch.Balances[1].LimitCents != 100000 {
		t.Errorf("card balance = %#v", batch.Balances[1])
	}

	if len(batch.Added) != 1 {
		t.Fatalf("added count = %d, want 1", len(batch.Added))
	}
	added := batch.Added[0]
	if added.AmountCents != -435 || added.Status != canon.TxnStatusPending {
		t.Errorf("added transaction amount/status = %d/%s, want -435/pending", added.AmountCents, added.Status)
	}
	if added.MerchantRaw != "Coffee Shop" || added.SourceCategory != "FOOD_AND_DRINK" {
		t.Errorf("added merchant/category = %q/%q", added.MerchantRaw, added.SourceCategory)
	}
	if len(batch.Modified) != 1 || batch.Modified[0].AmountCents != 101 {
		t.Errorf("modified transactions = %#v, want +101-cent inflow", batch.Modified)
	}
	if !reflect.DeepEqual(batch.Removed, []string{"removed-id"}) {
		t.Errorf("removed = %v, want removed-id", batch.Removed)
	}

	if len(batch.Liabilities) != 1 {
		t.Fatalf("liability count = %d, want 1", len(batch.Liabilities))
	}
	liability := batch.Liabilities[0]
	if liability.APR != 19.99 || liability.LimitCents != 100000 ||
		liability.MinPaymentCents != 101 || liability.LastStatementCents != 435 {
		t.Errorf("credit liability = %#v", liability)
	}
	if liability.StatementDay != 5 || liability.DueDay != 28 {
		t.Errorf("statement/due days = %d/%d, want 5/28", liability.StatementDay, liability.DueDay)
	}
}

func TestNormalizeCurrentBalanceUsesPlaidLiabilitySign(t *testing.T) {
	tests := []struct {
		name        string
		accountType canon.AccountType
		raw         float64
		want        int64
	}{
		{name: "credit card amount owed", accountType: canon.AccountTypeCreditCard, raw: 410, want: 41000},
		{name: "credit card in credit", accountType: canon.AccountTypeCreditCard, raw: -50, want: -5000},
		{name: "loan principal remaining", accountType: canon.AccountTypeLoan, raw: 65262, want: 6526200},
		{name: "asset sign passes through", accountType: canon.AccountTypeChecking, raw: -25, want: -2500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeCurrentBalanceToCents(test.accountType, &test.raw)
			if err != nil {
				t.Fatalf("normalizeCurrentBalanceToCents() error: %v", err)
			}
			if got != test.want {
				t.Errorf("normalizeCurrentBalanceToCents() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestProviderBalanceDateUsesLocalCalendar(t *testing.T) {
	current := 100.00
	provider := mustTestProvider(t, &fakeGateway{})
	provider.now = func() time.Time {
		return time.Date(
			2026,
			time.July,
			16,
			17,
			30,
			0,
			0,
			time.FixedZone("America/Los_Angeles", -7*60*60),
		)
	}

	_, balances, _, skipped := provider.normalizeAccounts([]rawAccount{{
		ID:       "checking-1",
		Name:     "Test Checking",
		Type:     "depository",
		Subtype:  "checking",
		Currency: "USD",
		Current:  &current,
	}})
	if len(skipped) != 0 {
		t.Fatalf("normalizeAccounts() skipped = %#v, want none", skipped)
	}
	if len(balances) != 1 {
		t.Fatalf("balance count = %d, want 1", len(balances))
	}
	if balances[0].Date != "2026-07-16" {
		t.Errorf("balance date = %q, want local 2026-07-16", balances[0].Date)
	}
}

func TestProviderSyncPreservesBalancesAcrossLocalCalendarDays(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	entityResult, err := db.Exec(
		"INSERT INTO entities (kind, name) VALUES ('personal', 'Test Personal')",
	)
	if err != nil {
		t.Fatalf("insert test entity: %v", err)
	}
	entityID, err := entityResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test entity id: %v", err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc
		) VALUES ('plaid', 'item-fake', 'Test Bank', x'010203')
	`)
	if err != nil {
		t.Fatalf("insert test provider item: %v", err)
	}
	providerItemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test provider item id: %v", err)
	}

	current := 100.00
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"":         {NextCursor: "cursor-1"},
			"cursor-1": {NextCursor: "cursor-2"},
		},
		accountsResult: []rawAccount{{
			ID:       "checking-1",
			Name:     "Test Checking",
			Type:     "depository",
			Subtype:  "checking",
			Currency: "USD",
			Current:  &current,
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)
	localZone := time.FixedZone("America/Los_Angeles", -7*60*60)
	now := time.Date(2026, time.July, 16, 17, 30, 0, 0, localZone)
	provider.now = func() time.Time { return now }
	ingestor := core.NewIngestor(db)
	target := core.SyncTarget{
		ProviderItemID:  providerItemID,
		DefaultEntityID: entityID,
	}

	firstBatch, err := provider.Sync(ctx, "")
	if err != nil {
		t.Fatalf("sync evening balance: %v", err)
	}
	if _, err := ingestor.ApplySync(ctx, target, firstBatch); err != nil {
		t.Fatalf("ingest evening balance: %v", err)
	}

	current = 200.00
	now = time.Date(2026, time.July, 17, 9, 0, 0, 0, localZone)
	target.ExpectedCursor = firstBatch.NextCursor
	secondBatch, err := provider.Sync(ctx, target.ExpectedCursor)
	if err != nil {
		t.Fatalf("sync next-morning balance: %v", err)
	}
	if _, err := ingestor.ApplySync(ctx, target, secondBatch); err != nil {
		t.Fatalf("ingest next-morning balance: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM balance_snapshots").Scan(&count); err != nil {
		t.Fatalf("count balance snapshots: %v", err)
	}
	if count != 2 {
		t.Errorf("balance snapshot count = %d, want 2", count)
	}
}

func TestProviderBalancesRequireCurrentAmount(t *testing.T) {
	current := 100.00
	available := 1500.00
	tests := []struct {
		name      string
		current   *float64
		available *float64
		wantCount int
	}{
		{
			name:      "available without current",
			available: &available,
			wantCount: 0,
		},
		{
			name:      "all amounts absent",
			wantCount: 0,
		},
		{
			name:      "current present",
			current:   &current,
			wantCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := mustTestProvider(t, &fakeGateway{})
			_, balances, _, skipped := provider.normalizeAccounts([]rawAccount{{
				ID:        "checking-1",
				Name:      "Test Checking",
				Type:      "depository",
				Subtype:   "checking",
				Currency:  "USD",
				Current:   test.current,
				Available: test.available,
			}})
			if len(skipped) != 0 {
				t.Fatalf("normalizeAccounts() skipped = %#v, want none", skipped)
			}
			if len(balances) != test.wantCount {
				t.Errorf("balance count = %d, want %d", len(balances), test.wantCount)
			}
			if test.wantCount == 1 && len(balances) == 1 && balances[0].CurrentCents != 10000 {
				t.Errorf("current cents = %d, want 10000", balances[0].CurrentCents)
			}
		})
	}
}

func TestProviderSyncThroughIngestReplacesPendingWithPosted(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	entityResult, err := db.Exec(
		"INSERT INTO entities (kind, name) VALUES ('personal', 'Test Personal')",
	)
	if err != nil {
		t.Fatalf("insert test entity: %v", err)
	}
	entityID, err := entityResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test entity id: %v", err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc
		) VALUES ('plaid', 'item-fake', 'Sandbox Bank', x'010203')
	`)
	if err != nil {
		t.Fatalf("insert test provider item: %v", err)
	}
	providerItemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test provider item id: %v", err)
	}

	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"": {
				Added: []rawTransaction{{
					ID:        "pending-id",
					AccountID: "checking-1",
					Date:      "2026-07-01",
					Amount:    4.35,
					Name:      "Coffee Shop",
					Pending:   true,
					Currency:  "USD",
				}},
				NextCursor: "cursor-1",
			},
			"cursor-1": {
				Added: []rawTransaction{{
					ID:        "posted-id",
					PendingID: "pending-id",
					AccountID: "checking-1",
					Date:      "2026-07-03",
					Amount:    4.35,
					Name:      "Coffee Shop",
					Currency:  "USD",
				}},
				Removed:    []string{"pending-id"},
				NextCursor: "cursor-2",
			},
		},
		accountsResult: []rawAccount{{
			ID:       "checking-1",
			Name:     "Test Checking",
			Type:     "depository",
			Subtype:  "checking",
			Currency: "USD",
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)
	ingestor := core.NewIngestor(db)
	target := core.SyncTarget{
		ProviderItemID:  providerItemID,
		DefaultEntityID: entityID,
	}

	firstBatch, err := provider.Sync(ctx, "")
	if err != nil {
		t.Fatalf("sync pending batch: %v", err)
	}
	if _, err := ingestor.ApplySync(ctx, target, firstBatch); err != nil {
		t.Fatalf("ingest pending batch: %v", err)
	}

	target.ExpectedCursor = firstBatch.NextCursor
	secondBatch, err := provider.Sync(ctx, target.ExpectedCursor)
	if err != nil {
		t.Fatalf("sync posted batch: %v", err)
	}
	if _, err := ingestor.ApplySync(ctx, target, secondBatch); err != nil {
		t.Fatalf("ingest posted batch: %v", err)
	}

	var count int
	var date, status string
	var amountCents int64
	if err := db.QueryRow(`
		SELECT count(*), date, status, amount_cents
		FROM transactions
	`).Scan(&count, &date, &status, &amountCents); err != nil {
		t.Fatalf("read canonical transaction: %v", err)
	}
	if count != 1 || date != "2026-07-03" || status != "posted" || amountCents != -435 {
		t.Errorf(
			"canonical transaction = count %d, date %s, status %s, cents %d",
			count,
			date,
			status,
			amountCents,
		)
	}

	var providerTransactionID, pendingTransactionID, cursor string
	if err := db.QueryRow(`
		SELECT provider_txn_id, pending_txn_id
		FROM txn_provider_refs
	`).Scan(&providerTransactionID, &pendingTransactionID); err != nil {
		t.Fatalf("read provider transaction reference: %v", err)
	}
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		providerItemID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read sync cursor: %v", err)
	}
	if providerTransactionID != "posted-id" || pendingTransactionID != "pending-id" || cursor != "cursor-2" {
		t.Errorf(
			"provider reference/cursor = %s/%s/%s",
			providerTransactionID,
			pendingTransactionID,
			cursor,
		)
	}
}

func TestProviderSyncRestartsWholePaginationLoopOnMutation(t *testing.T) {
	call := 0
	gateway := &fakeGateway{
		syncFunc: func(_ context.Context, _, cursor string) (rawSyncPage, error) {
			call++
			switch call {
			case 1:
				return rawSyncPage{
					Added:      []rawTransaction{{ID: "discard-me", AccountID: "checking-1", Date: "2026-07-01", Currency: "USD"}},
					NextCursor: "unstable-page-2",
					HasMore:    true,
				}, nil
			case 2:
				return rawSyncPage{}, &APIError{Code: errorSyncMutation}
			case 3:
				if cursor != "start" {
					t.Fatalf("restart cursor = %q, want start", cursor)
				}
				return rawSyncPage{
					Added:      []rawTransaction{{ID: "keep-me", AccountID: "checking-1", Date: "2026-07-02", Currency: "USD"}},
					NextCursor: "stable-final",
				}, nil
			default:
				t.Fatalf("unexpected sync call %d", call)
				return rawSyncPage{}, nil
			}
		},
		accountsResult: []rawAccount{{
			ID: "checking-1", Name: "Test Checking", Type: "depository", Subtype: "checking", Currency: "USD",
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "start")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Added) != 1 || batch.Added[0].ProviderTxnID != "keep-me" {
		t.Fatalf("added after restart = %#v, want only keep-me", batch.Added)
	}
	if batch.NextCursor != "stable-final" {
		t.Errorf("NextCursor = %q, want stable-final", batch.NextCursor)
	}
	if !reflect.DeepEqual(gateway.syncCursors, []string{"start", "unstable-page-2", "start"}) {
		t.Errorf("sync cursors = %v", gateway.syncCursors)
	}
}

func TestProviderSyncTreatsUnavailableLiabilitiesAsOptional(t *testing.T) {
	for _, code := range []string{
		"NO_LIABILITY_ACCOUNTS",
		"PRODUCTS_NOT_SUPPORTED",
		"PRODUCT_NOT_ENABLED",
		"ACCESS_NOT_GRANTED",
		"ADDITIONAL_CONSENT_REQUIRED",
		// Plaid returns PRODUCT_NOT_READY while a product's initial pull is
		// still running shortly after link; liabilities arrive on a later
		// sync like the other optional-product cases.
		"PRODUCT_NOT_READY",
	} {
		t.Run(code, func(t *testing.T) {
			gateway := &fakeGateway{
				syncPages: map[string]rawSyncPage{"": {NextCursor: "cursor"}},
				accountsResult: []rawAccount{{
					ID:       "checking-1",
					Name:     "Test Checking",
					Type:     "depository",
					Subtype:  "checking",
					Currency: "USD",
				}},
				liabilitiesError: &APIError{Code: code},
			}
			provider := mustTestProvider(t, gateway)
			batch, err := provider.Sync(context.Background(), "")
			if err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			if len(batch.Accounts) != 1 || len(batch.Liabilities) != 0 {
				t.Errorf("Sync() batch = %#v", batch)
			}
		})
	}
}

func TestProviderSyncReturnsLoginRequiredWithoutExposingToken(t *testing.T) {
	gateway := &fakeGateway{
		syncError: &APIError{Code: errorItemLoginRequired},
	}
	provider := mustTestProvider(t, gateway)

	_, err := provider.Sync(context.Background(), "")
	if errorCode(err) != errorItemLoginRequired {
		t.Fatalf("Sync() error = %v, want ITEM_LOGIN_REQUIRED", err)
	}
	if strings.Contains(err.Error(), fakeAccessToken) {
		t.Fatal("Sync() error exposes access token")
	}
}

func TestProviderConnectionsReportsItemHealth(t *testing.T) {
	tests := []struct {
		name      string
		item      rawItem
		itemError error
		wantState string
		wantCode  string
	}{
		{name: "healthy", item: rawItem{ID: "item-from-api", Institution: "API Bank"}, wantState: "ok"},
		{
			name:      "login required in item",
			item:      rawItem{ID: "item-from-api", Institution: "API Bank", ErrorCode: errorItemLoginRequired},
			wantState: "login_required",
			wantCode:  errorItemLoginRequired,
		},
		{
			name:      "login required response error",
			itemError: &APIError{Code: errorItemLoginRequired},
			wantState: "login_required",
			wantCode:  errorItemLoginRequired,
		},
		{
			name:      "other item error",
			item:      rawItem{ErrorCode: "INSTITUTION_DOWN"},
			wantState: "error",
			wantCode:  "INSTITUTION_DOWN",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := mustTestProvider(t, &fakeGateway{itemResult: test.item, itemError: test.itemError})
			statuses, err := provider.Connections(context.Background())
			if err != nil {
				t.Fatalf("Connections() error: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("status count = %d, want 1", len(statuses))
			}
			if statuses[0].State != test.wantState || statuses[0].Detail != test.wantCode {
				t.Errorf("status = %#v, want state/code %s/%s", statuses[0], test.wantState, test.wantCode)
			}
		})
	}
}

func TestProviderSyncNormalizesStudentAndMortgageLiabilities(t *testing.T) {
	studentMinimum := 150.005
	studentStatement := 1000.005
	mortgageRate := 6.125
	mortgagePayment := 2500.005
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{"": {NextCursor: "cursor"}},
		accountsResult: []rawAccount{
			{ID: "student-1", Name: "Student Loan", Type: "loan", Subtype: "student", Currency: "USD"},
			{ID: "mortgage-1", Name: "Mortgage", Type: "loan", Subtype: "mortgage", Currency: "USD"},
		},
		liabilitiesResult: rawLiabilities{
			Student: []rawStudentLiability{{
				AccountID:              "student-1",
				InterestRatePercentage: 4.35,
				MinimumPayment:         &studentMinimum,
				LastStatementBalance:   &studentStatement,
				LastStatementIssueDate: "2026-07-02",
				NextPaymentDueDate:     "2026-07-20",
			}},
			Mortgage: []rawMortgageLiability{{
				AccountID:          "mortgage-1",
				InterestPercentage: &mortgageRate,
				NextMonthlyPayment: &mortgagePayment,
				NextPaymentDueDate: "2026-07-15",
			}},
		},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Liabilities) != 2 {
		t.Fatalf("liability count = %d, want 2", len(batch.Liabilities))
	}
	student := batch.Liabilities[0]
	if student.APR != 4.35 || student.MinPaymentCents != 15001 ||
		student.LastStatementCents != 100001 || student.DueDay != 20 {
		t.Errorf("student liability = %#v", student)
	}
	mortgage := batch.Liabilities[1]
	if mortgage.APR != 6.125 || mortgage.MinPaymentCents != 250001 || mortgage.DueDay != 15 {
		t.Errorf("mortgage liability = %#v", mortgage)
	}
}

func TestProviderSyncSkipsUnsupportedCurrencyTransactions(t *testing.T) {
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"": {
				Added: []rawTransaction{
					{
						ID:        "good-id",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    4.35,
						Name:      "Coffee Shop",
						Currency:  "USD",
					},
					{
						ID:        "euro-id",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    10.00,
						Name:      "Cafe",
						Currency:  "EUR",
					},
					{
						ID:                 "unofficial-id",
						AccountID:          "checking-1",
						Date:               "2026-07-01",
						Amount:             5.00,
						Name:               "Crypto Shop",
						UnofficialCurrency: "BTC",
					},
				},
				NextCursor: "cursor-1",
			},
		},
		accountsResult: []rawAccount{{
			ID:       "checking-1",
			Name:     "Test Checking",
			Type:     "depository",
			Subtype:  "checking",
			Currency: "USD",
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Added) != 1 || batch.Added[0].ProviderTxnID != "good-id" {
		t.Fatalf("added transactions = %#v, want only good-id", batch.Added)
	}
	if batch.NextCursor != "cursor-1" {
		t.Errorf("NextCursor = %q, want cursor-1", batch.NextCursor)
	}
	if len(batch.Skipped) != 2 {
		t.Fatalf("skipped count = %d, want 2: %#v", len(batch.Skipped), batch.Skipped)
	}
	skippedByID := make(map[string]canon.SkippedRecord, len(batch.Skipped))
	for _, skipped := range batch.Skipped {
		if skipped.Kind != canon.RecordKindTransaction {
			t.Errorf("skipped kind = %q, want transaction", skipped.Kind)
		}
		if skipped.Reason != canon.SkipUnsupportedCurrency {
			t.Errorf("skipped reason = %q, want unsupported_currency", skipped.Reason)
		}
		skippedByID[skipped.ID] = skipped
	}
	if skippedByID["euro-id"].Detail != "EUR" {
		t.Errorf("euro-id detail = %q, want EUR", skippedByID["euro-id"].Detail)
	}
	if _, found := skippedByID["unofficial-id"]; !found {
		t.Error("unofficial-currency transaction was not recorded as skipped")
	}
}

func TestProviderSyncSkipsMalformedDateTransactions(t *testing.T) {
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"": {
				Added: []rawTransaction{
					{
						ID:        "good-id",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    4.35,
						Name:      "Coffee Shop",
						Currency:  "USD",
					},
					{
						ID:        "no-date-id",
						AccountID: "checking-1",
						Amount:    3.00,
						Name:      "Vending",
						Currency:  "USD",
					},
					{
						ID:        "bad-date-id",
						AccountID: "checking-1",
						Date:      "07/01/2026",
						Amount:    2.00,
						Name:      "Kiosk",
						Currency:  "USD",
					},
				},
				NextCursor: "cursor-1",
			},
		},
		accountsResult: []rawAccount{{
			ID:       "checking-1",
			Name:     "Test Checking",
			Type:     "depository",
			Subtype:  "checking",
			Currency: "USD",
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Added) != 1 || batch.Added[0].ProviderTxnID != "good-id" {
		t.Fatalf("added transactions = %#v, want only good-id", batch.Added)
	}
	if batch.NextCursor != "cursor-1" {
		t.Errorf("NextCursor = %q, want cursor-1", batch.NextCursor)
	}
	if len(batch.Skipped) != 2 {
		t.Fatalf("skipped count = %d, want 2: %#v", len(batch.Skipped), batch.Skipped)
	}
	for _, skipped := range batch.Skipped {
		if skipped.Kind != canon.RecordKindTransaction ||
			skipped.Reason != canon.SkipMalformedRecord ||
			(skipped.ID != "no-date-id" && skipped.ID != "bad-date-id") {
			t.Errorf("skipped record = %#v, want no-date-id/bad-date-id malformed_record", skipped)
		}
	}
}

func TestProviderSyncSkipsUnsupportedAccountTypeAndDependentRecords(t *testing.T) {
	current := 100.00
	minimum := 25.00
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"": {
				Added: []rawTransaction{
					{
						ID:        "good-txn",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    1.00,
						Name:      "Coffee Shop",
						Currency:  "USD",
					},
					{
						ID:        "custody-txn",
						AccountID: "custodial-1",
						Date:      "2026-07-01",
						Amount:    2.00,
						Name:      "Brokerage",
						Currency:  "USD",
					},
				},
				NextCursor: "cursor-1",
			},
		},
		accountsResult: []rawAccount{
			{
				ID:       "checking-1",
				Name:     "Test Checking",
				Type:     "depository",
				Subtype:  "checking",
				Currency: "USD",
			},
			{
				ID:       "custodial-1",
				Name:     "Test Custodial",
				Type:     "custodial",
				Subtype:  "custodial",
				Currency: "USD",
				Current:  &current,
			},
		},
		liabilitiesResult: rawLiabilities{
			Credit: []rawCreditLiability{{
				AccountID:          "custodial-1",
				MinimumPayment:     &minimum,
				NextPaymentDueDate: "2026-07-28",
			}},
		},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Accounts) != 1 || batch.Accounts[0].ProviderAccountID != "checking-1" {
		t.Fatalf("accounts = %#v, want only checking-1", batch.Accounts)
	}
	if len(batch.Added) != 1 || batch.Added[0].ProviderTxnID != "good-txn" {
		t.Fatalf("added transactions = %#v, want only good-txn", batch.Added)
	}
	if len(batch.Liabilities) != 0 {
		t.Fatalf("liabilities = %#v, want none", batch.Liabilities)
	}
	if len(batch.Skipped) != 3 {
		t.Fatalf("skipped count = %d, want 3: %#v", len(batch.Skipped), batch.Skipped)
	}
	var accountSkip, txnSkip, liabilitySkip *canon.SkippedRecord
	for index := range batch.Skipped {
		skipped := &batch.Skipped[index]
		switch skipped.Kind {
		case canon.RecordKindAccount:
			accountSkip = skipped
		case canon.RecordKindTransaction:
			txnSkip = skipped
		case canon.RecordKindLiability:
			liabilitySkip = skipped
		default:
			t.Errorf("unexpected skipped record %#v", skipped)
		}
	}
	if accountSkip == nil || accountSkip.ID != "custodial-1" ||
		accountSkip.Reason != canon.SkipUnsupportedAccountType {
		t.Errorf("account skip = %#v, want custodial-1 unsupported_account_type", accountSkip)
	}
	if txnSkip == nil || txnSkip.ID != "custody-txn" || txnSkip.Reason != canon.SkipAccountSkipped {
		t.Errorf("transaction skip = %#v, want custody-txn account_skipped", txnSkip)
	}
	if liabilitySkip == nil || liabilitySkip.ID != "custodial-1" ||
		liabilitySkip.Reason != canon.SkipAccountSkipped {
		t.Errorf("liability skip = %#v, want custodial-1 account_skipped", liabilitySkip)
	}
}

func TestProviderSyncSkipsMalformedLiability(t *testing.T) {
	minimum := 25.00
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{"": {NextCursor: "cursor-1"}},
		accountsResult: []rawAccount{
			{ID: "card-1", Name: "Test Card One", Type: "credit", Subtype: "credit card", Currency: "USD"},
			{ID: "card-2", Name: "Test Card Two", Type: "credit", Subtype: "credit card", Currency: "USD"},
		},
		liabilitiesResult: rawLiabilities{
			Credit: []rawCreditLiability{
				{
					AccountID:          "card-1",
					MinimumPayment:     &minimum,
					NextPaymentDueDate: "07/28/2026",
				},
				{
					AccountID:          "card-2",
					MinimumPayment:     &minimum,
					NextPaymentDueDate: "2026-07-28",
				},
			},
		},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Liabilities) != 1 || batch.Liabilities[0].AccountRef != "card-2" {
		t.Fatalf("liabilities = %#v, want only card-2", batch.Liabilities)
	}
	if len(batch.Skipped) != 1 {
		t.Fatalf("skipped count = %d, want 1: %#v", len(batch.Skipped), batch.Skipped)
	}
	skipped := batch.Skipped[0]
	if skipped.Kind != canon.RecordKindLiability || skipped.ID != "card-1" ||
		skipped.Reason != canon.SkipMalformedRecord {
		t.Errorf("skipped record = %#v, want card-1 liability malformed_record", skipped)
	}
}

// TestProviderSyncDoesNotSkipCountARecoveredLiability pins the de-dup:
// an account malformed in credit[] but valid in student[] merges one valid
// liability and records no skip for that account.
func TestProviderSyncDoesNotSkipCountARecoveredLiability(t *testing.T) {
	minimum := 25.00
	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{"": {NextCursor: "cursor-1"}},
		accountsResult: []rawAccount{
			{ID: "acct-1", Name: "Test Account", Type: "depository", Subtype: "checking", Currency: "USD"},
		},
		liabilitiesResult: rawLiabilities{
			Credit: []rawCreditLiability{{
				AccountID:          "acct-1",
				MinimumPayment:     &minimum,
				NextPaymentDueDate: "07/28/2026", // malformed: skipped in credit[]
			}},
			Student: []rawStudentLiability{{
				AccountID:              "acct-1",
				InterestRatePercentage: 5.25,
				MinimumPayment:         &minimum,
				NextPaymentDueDate:     "2026-07-28", // valid: merged from student[]
			}},
		},
	}
	provider := mustTestProvider(t, gateway)

	batch, err := provider.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if len(batch.Liabilities) != 1 || batch.Liabilities[0].AccountRef != "acct-1" {
		t.Fatalf("liabilities = %#v, want the one valid acct-1 liability", batch.Liabilities)
	}
	for _, skipped := range batch.Skipped {
		if skipped.ID == "acct-1" {
			t.Errorf("acct-1 was skip-counted despite merging a valid liability: %#v", skipped)
		}
	}
}

func TestProviderSyncPoisonRecordDoesNotWedgeItem(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	entityResult, err := db.Exec(
		"INSERT INTO entities (kind, name) VALUES ('personal', 'Test Personal')",
	)
	if err != nil {
		t.Fatalf("insert test entity: %v", err)
	}
	entityID, err := entityResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test entity id: %v", err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc
		) VALUES ('plaid', 'item-fake', 'Sandbox Bank', x'010203')
	`)
	if err != nil {
		t.Fatalf("insert test provider item: %v", err)
	}
	providerItemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("read test provider item id: %v", err)
	}

	gateway := &fakeGateway{
		syncPages: map[string]rawSyncPage{
			"": {
				Added: []rawTransaction{
					{
						ID:        "good-id",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    4.35,
						Name:      "Coffee Shop",
						Currency:  "USD",
					},
					{
						ID:        "poison-id",
						AccountID: "checking-1",
						Date:      "2026-07-01",
						Amount:    9.99,
						Name:      "Cafe",
						Currency:  "EUR",
					},
				},
				NextCursor: "cursor-1",
			},
			"cursor-1": {
				Added: []rawTransaction{{
					ID:        "next-good-id",
					AccountID: "checking-1",
					Date:      "2026-07-02",
					Amount:    2.50,
					Name:      "Bookstore",
					Currency:  "USD",
				}},
				NextCursor: "cursor-2",
			},
		},
		accountsResult: []rawAccount{{
			ID:       "checking-1",
			Name:     "Test Checking",
			Type:     "depository",
			Subtype:  "checking",
			Currency: "USD",
		}},
		liabilitiesError: &APIError{Code: "NO_LIABILITY_ACCOUNTS"},
	}
	provider := mustTestProvider(t, gateway)
	ingestor := core.NewIngestor(db)
	target := core.SyncTarget{
		ProviderItemID:  providerItemID,
		DefaultEntityID: entityID,
	}

	firstBatch, err := provider.Sync(ctx, "")
	if err != nil {
		t.Fatalf("sync poisoned batch: %v", err)
	}
	if len(firstBatch.Added) != 1 || len(firstBatch.Skipped) != 1 {
		t.Fatalf(
			"first sync added/skipped = %d/%d, want 1/1",
			len(firstBatch.Added),
			len(firstBatch.Skipped),
		)
	}
	if _, err := ingestor.ApplySync(ctx, target, firstBatch); err != nil {
		t.Fatalf("ingest poisoned batch: %v", err)
	}

	target.ExpectedCursor = firstBatch.NextCursor
	secondBatch, err := provider.Sync(ctx, target.ExpectedCursor)
	if err != nil {
		t.Fatalf("re-sync after poisoned batch: %v", err)
	}
	if _, err := ingestor.ApplySync(ctx, target, secondBatch); err != nil {
		t.Fatalf("ingest second batch: %v", err)
	}

	var count int
	var cursor string
	if err := db.QueryRow("SELECT count(*) FROM transactions").Scan(&count); err != nil {
		t.Fatalf("count stored transactions: %v", err)
	}
	if err := db.QueryRow(
		"SELECT sync_cursor FROM provider_items WHERE id = ?",
		providerItemID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read sync cursor: %v", err)
	}
	if count != 2 || cursor != "cursor-2" {
		t.Errorf("stored transactions/cursor = %d/%q, want 2/cursor-2", count, cursor)
	}
}

func TestNewProviderValidationDoesNotExposeAccessToken(t *testing.T) {
	_, err := newProvider(&fakeGateway{}, "item-1", "Sandbox Bank", " "+fakeAccessToken)
	if err == nil {
		t.Fatal("newProvider() accepted access token whitespace")
	}
	if strings.Contains(err.Error(), fakeAccessToken) {
		t.Fatal("newProvider() error exposes access token")
	}
}

type fakeGateway struct {
	syncPages         map[string]rawSyncPage
	syncFunc          func(context.Context, string, string) (rawSyncPage, error)
	syncError         error
	syncCursors       []string
	accountsResult    []rawAccount
	accountsError     error
	liabilitiesResult rawLiabilities
	liabilitiesError  error
	itemResult        rawItem
	itemError         error
}

func (g *fakeGateway) transactionsSync(
	ctx context.Context,
	accessToken string,
	cursor string,
) (rawSyncPage, error) {
	if accessToken != fakeAccessToken {
		return rawSyncPage{}, errors.New("unexpected test access token")
	}
	g.syncCursors = append(g.syncCursors, cursor)
	if g.syncFunc != nil {
		return g.syncFunc(ctx, accessToken, cursor)
	}
	if g.syncError != nil {
		return rawSyncPage{}, g.syncError
	}
	return g.syncPages[cursor], nil
}

func (g *fakeGateway) accounts(context.Context, string) ([]rawAccount, error) {
	return g.accountsResult, g.accountsError
}

func (g *fakeGateway) liabilities(context.Context, string) (rawLiabilities, error) {
	return g.liabilitiesResult, g.liabilitiesError
}

func (g *fakeGateway) item(context.Context, string) (rawItem, error) {
	return g.itemResult, g.itemError
}

func mustTestProvider(t *testing.T, gateway gateway) *Provider {
	t.Helper()

	provider, err := newProvider(gateway, "item-1", "Sandbox Bank", fakeAccessToken)
	if err != nil {
		t.Fatalf("newProvider() error: %v", err)
	}
	return provider
}

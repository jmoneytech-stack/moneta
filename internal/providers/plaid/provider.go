package plaid

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

const maxPaginationRestarts = 3

var _ canon.Provider = (*Provider)(nil)

// Provider is one Plaid Item bound to the canonical provider contract.
type Provider struct {
	gateway     gateway
	accessToken string
	itemID      string
	institution string
	now         func() time.Time
}

// New creates a Plaid provider for one Item and access token.
func New(
	config Config,
	itemID string,
	institution string,
	accessToken string,
) (*Provider, error) {
	client, err := newSDKClient(config, nil)
	if err != nil {
		return nil, err
	}
	return newProvider(
		&sdkGateway{client: client},
		itemID,
		institution,
		accessToken,
	)
}

func newProvider(
	gateway gateway,
	itemID string,
	institution string,
	accessToken string,
) (*Provider, error) {
	if gateway == nil {
		return nil, fmt.Errorf("Plaid gateway is required")
	}
	if strings.TrimSpace(itemID) == "" {
		return nil, fmt.Errorf("Plaid item id is required")
	}
	if err := validateOpaqueToken("access token", accessToken); err != nil {
		return nil, err
	}
	return &Provider{
		gateway:     gateway,
		accessToken: accessToken,
		itemID:      itemID,
		institution: institution,
		now:         time.Now,
	}, nil
}

func (p *Provider) Name() string {
	return providerName
}

func (p *Provider) Capabilities() canon.Capability {
	return canon.CapAccounts |
		canon.CapTransactions |
		canon.CapBalances |
		canon.CapLiabilities
}

func (p *Provider) Connections(ctx context.Context) ([]canon.ConnectionStatus, error) {
	item, err := p.gateway.item(ctx, p.accessToken)
	if err != nil {
		if errorCode(err) == errorItemLoginRequired {
			return []canon.ConnectionStatus{{
				ID:          p.itemID,
				Institution: p.institution,
				State:       "login_required",
				Detail:      errorItemLoginRequired,
			}}, nil
		}
		return nil, err
	}

	itemID := item.ID
	if itemID == "" {
		itemID = p.itemID
	}
	institution := item.Institution
	if institution == "" {
		institution = p.institution
	}
	if item.ErrorCode == "" {
		return []canon.ConnectionStatus{{
			ID:          itemID,
			Institution: institution,
			State:       "ok",
		}}, nil
	}
	state := "error"
	if item.ErrorCode == errorItemLoginRequired {
		state = "login_required"
	}
	return []canon.ConnectionStatus{{
		ID:          itemID,
		Institution: institution,
		State:       state,
		Detail:      item.ErrorCode,
	}}, nil
}

func (p *Provider) Sync(ctx context.Context, cursor string) (*canon.SyncBatch, error) {
	updates, err := p.transactionUpdates(ctx, cursor)
	if err != nil {
		return nil, err
	}

	accounts, err := p.gateway.accounts(ctx, p.accessToken)
	if err != nil {
		return nil, err
	}
	updates.mergeAccounts(accounts, true)

	liabilities, err := p.gateway.liabilities(ctx, p.accessToken)
	if err != nil {
		if !liabilitiesUnavailable(err) {
			return nil, err
		}
		liabilities = rawLiabilities{}
	}
	updates.mergeAccounts(liabilities.Accounts, false)

	canonicalAccounts, balances, accountByID, err := p.normalizeAccounts(updates.accountsInOrder())
	if err != nil {
		return nil, err
	}
	added, err := normalizeTransactions(updates.added)
	if err != nil {
		return nil, fmt.Errorf("normalize added Plaid transactions: %w", err)
	}
	modified, err := normalizeTransactions(updates.modified)
	if err != nil {
		return nil, fmt.Errorf("normalize modified Plaid transactions: %w", err)
	}
	canonicalLiabilities, err := normalizeLiabilities(liabilities, accountByID)
	if err != nil {
		return nil, err
	}

	return &canon.SyncBatch{
		Accounts:    canonicalAccounts,
		Added:       added,
		Modified:    modified,
		Removed:     updates.removed,
		Balances:    balances,
		Liabilities: canonicalLiabilities,
		NextCursor:  updates.nextCursor,
	}, nil
}

func (p *Provider) transactionUpdates(ctx context.Context, cursor string) (*syncAccumulator, error) {
	for restart := 0; restart <= maxPaginationRestarts; restart++ {
		updates := newSyncAccumulator()
		pageCursor := cursor
		for {
			page, err := p.gateway.transactionsSync(ctx, p.accessToken, pageCursor)
			if err != nil {
				if errorCode(err) == errorSyncMutation && restart < maxPaginationRestarts {
					break
				}
				return nil, err
			}
			updates.addPage(page)
			if !page.HasMore {
				return updates, nil
			}
			if page.NextCursor == "" || page.NextCursor == pageCursor {
				return nil, fmt.Errorf("Plaid pagination returned an invalid next cursor")
			}
			pageCursor = page.NextCursor
		}
	}
	return nil, errors.New("Plaid pagination restart limit exceeded")
}

type syncAccumulator struct {
	accounts     map[string]rawAccount
	accountOrder []string
	added        []rawTransaction
	modified     []rawTransaction
	removed      []string
	nextCursor   string
}

func newSyncAccumulator() *syncAccumulator {
	return &syncAccumulator{accounts: make(map[string]rawAccount)}
}

func (a *syncAccumulator) addPage(page rawSyncPage) {
	a.mergeAccounts(page.Accounts, true)
	a.added = append(a.added, page.Added...)
	a.modified = append(a.modified, page.Modified...)
	a.removed = append(a.removed, page.Removed...)
	a.nextCursor = page.NextCursor
}

func (a *syncAccumulator) mergeAccounts(accounts []rawAccount, overwrite bool) {
	for _, account := range accounts {
		_, exists := a.accounts[account.ID]
		if exists && !overwrite {
			continue
		}
		if !exists {
			a.accountOrder = append(a.accountOrder, account.ID)
		}
		a.accounts[account.ID] = account
	}
}

func (a *syncAccumulator) accountsInOrder() []rawAccount {
	accounts := make([]rawAccount, 0, len(a.accountOrder))
	for _, accountID := range a.accountOrder {
		accounts = append(accounts, a.accounts[accountID])
	}
	return accounts
}

func (p *Provider) normalizeAccounts(
	accounts []rawAccount,
) ([]canon.Account, []canon.Balance, map[string]rawAccount, error) {
	canonicalAccounts := make([]canon.Account, 0, len(accounts))
	balances := make([]canon.Balance, 0, len(accounts))
	accountByID := make(map[string]rawAccount, len(accounts))
	balanceDate := canon.Date(p.now().Format("2006-01-02"))

	for _, account := range accounts {
		if account.ID == "" {
			return nil, nil, nil, fmt.Errorf("Plaid account id is empty")
		}
		accountType, err := canonicalAccountType(account.Type, account.Subtype)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("normalize Plaid account %q: %w", account.ID, err)
		}
		currency, err := canonicalPlaidCurrency(account.Currency, account.UnofficialCurrency)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("normalize Plaid account %q: %w", account.ID, err)
		}
		name := account.Name
		if name == "" {
			name = account.OfficialName
		}
		canonicalAccounts = append(canonicalAccounts, canon.Account{
			ProviderAccountID: account.ID,
			Name:              name,
			Institution:       p.institution,
			Mask:              account.Mask,
			Type:              accountType,
			Currency:          currency,
		})
		accountByID[account.ID] = account

		if account.Current == nil {
			continue
		}
		current, err := optionalMoneyToCents(account.Current)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("normalize current balance for %q: %w", account.ID, err)
		}
		available, err := optionalMoneyToCents(account.Available)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("normalize available balance for %q: %w", account.ID, err)
		}
		limit, err := optionalMoneyToCents(account.Limit)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("normalize balance limit for %q: %w", account.ID, err)
		}
		balances = append(balances, canon.Balance{
			AccountRef:     account.ID,
			Date:           balanceDate,
			CurrentCents:   current,
			AvailableCents: available,
			LimitCents:     limit,
		})
	}
	return canonicalAccounts, balances, accountByID, nil
}

package plaid

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const (
	defaultClientUserID = "moneta-local-owner"
	maxOpaqueTokenBytes = 4096
	maxInstitutionRunes = 200
	providerName        = "plaid"
)

// LinkedItem is the safe result of a completed Plaid Link session. It never
// contains the public token or permanent access token.
type LinkedItem struct {
	DatabaseID  int64  `json:"database_id"`
	ItemID      string `json:"item_id"`
	Institution string `json:"institution"`
}

// Linker creates Link sessions, exchanges public tokens, encrypts permanent
// access tokens, and persists the resulting Plaid Item.
type Linker struct {
	gateway linkGateway
	db      *sql.DB
	cipher  *secret.Cipher
}

// NewLinker creates the Plaid Link application service.
func NewLinker(config Config, db *sql.DB, cipher *secret.Cipher) (*Linker, error) {
	client, err := newSDKClient(config, nil)
	if err != nil {
		return nil, err
	}
	return newLinker(&sdkGateway{client: client}, db, cipher)
}

func newLinker(gateway linkGateway, db *sql.DB, cipher *secret.Cipher) (*Linker, error) {
	if gateway == nil {
		return nil, fmt.Errorf("Plaid Link gateway is required")
	}
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if cipher == nil {
		return nil, fmt.Errorf("secret cipher is required")
	}
	return &Linker{gateway: gateway, db: db, cipher: cipher}, nil
}

// CreateLinkToken creates a short-lived token for the embedded Link page.
func (l *Linker) CreateLinkToken(ctx context.Context) (string, error) {
	token, err := l.gateway.createLinkToken(ctx, defaultClientUserID)
	if err != nil {
		return "", err
	}
	if err := validateOpaqueToken("link token", token); err != nil {
		return "", err
	}
	return token, nil
}

// CompleteLink exchanges a browser-provided public token and stores only the
// encrypted permanent access token.
func (l *Linker) CompleteLink(
	ctx context.Context,
	publicToken string,
	institution string,
) (LinkedItem, error) {
	if err := validateOpaqueToken("public token", publicToken); err != nil {
		return LinkedItem{}, err
	}
	institution, err := validateInstitution(institution)
	if err != nil {
		return LinkedItem{}, err
	}

	exchange, err := l.gateway.exchangePublicToken(ctx, publicToken)
	if err != nil {
		return LinkedItem{}, err
	}
	if err := validateOpaqueToken("Item id", exchange.ItemID); err != nil {
		return LinkedItem{}, err
	}
	if err := validateOpaqueToken("access token", exchange.AccessToken); err != nil {
		return LinkedItem{}, err
	}

	plaintext := []byte(exchange.AccessToken)
	defer clear(plaintext)
	ciphertext, err := l.cipher.Seal(plaintext)
	if err != nil {
		return LinkedItem{}, fmt.Errorf("encrypt Plaid access token: %w", err)
	}
	databaseID, err := store.SaveProviderItem(ctx, l.db, store.ProviderItemSecret{
		Provider:              providerName,
		ItemID:                exchange.ItemID,
		Institution:           institution,
		AccessTokenCiphertext: ciphertext,
	})
	if err != nil {
		return LinkedItem{}, err
	}

	return LinkedItem{
		DatabaseID:  databaseID,
		ItemID:      exchange.ItemID,
		Institution: institution,
	}, nil
}

func validateOpaqueToken(kind, token string) error {
	if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 || len(token) > maxOpaqueTokenBytes {
		return fmt.Errorf("Plaid %s is invalid", kind)
	}
	return nil
}

func validateInstitution(institution string) (string, error) {
	institution = strings.TrimSpace(institution)
	if !utf8.ValidString(institution) || utf8.RuneCountInString(institution) > maxInstitutionRunes ||
		strings.IndexFunc(institution, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("Plaid institution name is invalid")
	}
	return institution, nil
}

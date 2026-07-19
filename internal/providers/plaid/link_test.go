package plaid

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/secret"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

const (
	fakeLinkToken      = "link-sandbox-fake-token"
	fakePublicToken    = "public-sandbox-fake-token"
	fakePermanentToken = "access-sandbox-fake-permanent-token"
)

func TestLinkerCreatesTokenAndPersistsOnlyEncryptedAccessToken(t *testing.T) {
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
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	gateway := &fakeLinkGateway{
		linkToken: fakeLinkToken,
		exchange: rawLinkExchange{
			ItemID:      "item-fake",
			AccessToken: fakePermanentToken,
		},
	}
	linker, err := newLinker(gateway, db, cipher)
	if err != nil {
		t.Fatalf("newLinker() error: %v", err)
	}

	linkToken, err := linker.CreateLinkToken(ctx)
	if err != nil {
		t.Fatalf("CreateLinkToken() error: %v", err)
	}
	if linkToken != fakeLinkToken || gateway.clientUserID != defaultClientUserID {
		t.Errorf("Link token/user = %q/%q", linkToken, gateway.clientUserID)
	}

	linked, err := linker.CompleteLink(ctx, fakePublicToken, "  Sandbox Bank  ")
	if err != nil {
		t.Fatalf("CompleteLink() error: %v", err)
	}
	if gateway.publicToken != fakePublicToken {
		t.Errorf("exchanged public token = %q", gateway.publicToken)
	}
	if linked.DatabaseID <= 0 || linked.ItemID != "item-fake" || linked.Institution != "Sandbox Bank" {
		t.Errorf("linked Item = %#v", linked)
	}

	var ciphertext []byte
	var itemID, institution string
	if err := db.QueryRowContext(ctx, `
		SELECT item_id, institution, access_token_enc
		FROM provider_items
	`).Scan(&itemID, &institution, &ciphertext); err != nil {
		t.Fatalf("read provider Item: %v", err)
	}
	if itemID != "item-fake" || institution != "Sandbox Bank" {
		t.Errorf("stored Item = %q/%q", itemID, institution)
	}
	if bytes.Equal(ciphertext, []byte(fakePermanentToken)) ||
		bytes.Contains(ciphertext, []byte(fakePermanentToken)) {
		t.Fatal("database contains the plaintext Plaid access token")
	}
	plaintext, err := cipher.Open(ciphertext)
	if err != nil {
		t.Fatalf("decrypt stored access token: %v", err)
	}
	defer clear(plaintext)
	if string(plaintext) != fakePermanentToken {
		t.Fatal("stored access token does not decrypt to the exchanged token")
	}
}

func TestLinkerRejectsInvalidBrowserInputBeforeExchange(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cipher, err := secret.NewCipher(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("create test cipher: %v", err)
	}
	gateway := &fakeLinkGateway{
		exchange: rawLinkExchange{ItemID: "item-fake", AccessToken: fakePermanentToken},
	}
	linker, err := newLinker(gateway, db, cipher)
	if err != nil {
		t.Fatalf("newLinker() error: %v", err)
	}

	tests := []struct {
		name        string
		publicToken string
		institution string
	}{
		{name: "empty public token", institution: "Sandbox Bank"},
		{name: "public token whitespace", publicToken: "public token", institution: "Sandbox Bank"},
		{name: "institution control", publicToken: fakePublicToken, institution: "Bad\nBank"},
		{name: "institution too long", publicToken: fakePublicToken, institution: strings.Repeat("x", maxInstitutionRunes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway.exchangeCalls = 0
			if _, err := linker.CompleteLink(ctx, test.publicToken, test.institution); err == nil {
				t.Fatal("CompleteLink() succeeded with invalid input")
			}
			if gateway.exchangeCalls != 0 {
				t.Fatal("invalid browser input reached Plaid exchange")
			}
		})
	}
}

type fakeLinkGateway struct {
	linkToken     string
	linkTokenErr  error
	clientUserID  string
	exchange      rawLinkExchange
	exchangeErr   error
	publicToken   string
	exchangeCalls int
}

func (g *fakeLinkGateway) createLinkToken(
	_ context.Context,
	clientUserID string,
) (string, error) {
	g.clientUserID = clientUserID
	return g.linkToken, g.linkTokenErr
}

func (g *fakeLinkGateway) exchangePublicToken(
	_ context.Context,
	publicToken string,
) (rawLinkExchange, error) {
	g.exchangeCalls++
	g.publicToken = publicToken
	return g.exchange, g.exchangeErr
}

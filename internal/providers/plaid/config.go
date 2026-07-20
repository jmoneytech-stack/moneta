package plaid

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	plaidSDK "github.com/plaid/plaid-go/v43/plaid"
)

const (
	clientIDEnvironment = "PLAID_CLIENT_ID"
	secretEnvironment   = "PLAID_SECRET"
	plaidEnvironment    = "PLAID_ENV"

	defaultRequestTimeout = 30 * time.Second
)

// Environment selects the Plaid API host.
type Environment string

const (
	EnvironmentSandbox    Environment = "sandbox"
	EnvironmentProduction Environment = "production"
)

var (
	ErrClientIDMissing = errors.New("PLAID_CLIENT_ID is required")
	ErrSecretMissing   = errors.New("PLAID_SECRET is required")
	ErrEnvironment     = errors.New("PLAID_ENV must be sandbox or production")
)

// Config contains validated Plaid credentials and environment selection.
// Fields remain private to prevent accidental exposure through ordinary JSON
// encoding or field access outside this provider package.
type Config struct {
	clientID    string
	secret      string
	environment Environment
}

// ConfigFromEnvironment reads Plaid configuration from environment variables.
// PLAID_ENV defaults to sandbox and must be explicitly set for production.
func ConfigFromEnvironment() (Config, error) {
	environment := Environment(os.Getenv(plaidEnvironment))
	if environment == "" {
		environment = EnvironmentSandbox
	}
	return NewConfig(
		os.Getenv(clientIDEnvironment),
		os.Getenv(secretEnvironment),
		environment,
	)
}

// NewConfig validates Plaid credentials without making a network request.
func NewConfig(clientID, secret string, environment Environment) (Config, error) {
	if strings.TrimSpace(clientID) == "" {
		return Config{}, ErrClientIDMissing
	}
	if strings.TrimSpace(secret) == "" {
		return Config{}, ErrSecretMissing
	}
	if strings.IndexFunc(clientID, unicode.IsSpace) >= 0 ||
		strings.IndexFunc(secret, unicode.IsSpace) >= 0 {
		return Config{}, fmt.Errorf("Plaid credentials must not contain whitespace")
	}
	if environment != EnvironmentSandbox && environment != EnvironmentProduction {
		return Config{}, ErrEnvironment
	}
	return Config{
		clientID:    clientID,
		secret:      secret,
		environment: environment,
	}, nil
}

// String returns a safe diagnostic representation with credentials omitted.
func (c Config) String() string {
	return fmt.Sprintf("Plaid config{environment:%s, credentials:redacted}", c.environment)
}

func newSDKClient(config Config, httpClient *http.Client) (*plaidSDK.APIClient, error) {
	configuration, err := newSDKConfiguration(config, httpClient)
	if err != nil {
		return nil, err
	}
	return plaidSDK.NewAPIClient(configuration), nil
}

func newSDKConfiguration(config Config, httpClient *http.Client) (*plaidSDK.Configuration, error) {
	if _, err := NewConfig(config.clientID, config.secret, config.environment); err != nil {
		return nil, err
	}

	configuration := plaidSDK.NewConfiguration()
	configuration.Debug = false
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", config.clientID)
	configuration.AddDefaultHeader("PLAID-SECRET", config.secret)
	switch config.environment {
	case EnvironmentSandbox:
		configuration.UseEnvironment(plaidSDK.Sandbox)
	case EnvironmentProduction:
		configuration.UseEnvironment(plaidSDK.Production)
	default:
		return nil, ErrEnvironment
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	configuration.HTTPClient = httpClient
	return configuration, nil
}

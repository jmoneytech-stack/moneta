package plaid

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	plaidSDK "github.com/plaid/plaid-go/v43/plaid"
)

const (
	fakeClientID = "fake-client-id"
	fakeSecret   = "fake-sandbox-secret"
)

func TestConfigFromEnvironmentDefaultsToSandbox(t *testing.T) {
	t.Setenv(clientIDEnvironment, fakeClientID)
	t.Setenv(secretEnvironment, fakeSecret)
	t.Setenv(plaidEnvironment, "")

	config, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("ConfigFromEnvironment() error: %v", err)
	}
	if config.environment != EnvironmentSandbox {
		t.Fatalf("environment = %q, want sandbox", config.environment)
	}
}

func TestConfigFromEnvironmentValidatesRequiredValues(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
		secret   string
		env      string
		wantErr  error
	}{
		{name: "missing client id", secret: fakeSecret, env: "sandbox", wantErr: ErrClientIDMissing},
		{name: "missing secret", clientID: fakeClientID, env: "sandbox", wantErr: ErrSecretMissing},
		{name: "invalid environment", clientID: fakeClientID, secret: fakeSecret, env: "development", wantErr: ErrEnvironment},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(clientIDEnvironment, test.clientID)
			t.Setenv(secretEnvironment, test.secret)
			t.Setenv(plaidEnvironment, test.env)

			_, err := ConfigFromEnvironment()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ConfigFromEnvironment() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestConfigDiagnosticStringRedactsCredentials(t *testing.T) {
	config, err := NewConfig(fakeClientID, fakeSecret, EnvironmentProduction)
	if err != nil {
		t.Fatalf("NewConfig() error: %v", err)
	}

	diagnostic := fmt.Sprintf("%v", config)
	if strings.Contains(diagnostic, fakeClientID) || strings.Contains(diagnostic, fakeSecret) {
		t.Fatalf("config diagnostic exposes credentials: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, "production") || !strings.Contains(diagnostic, "redacted") {
		t.Fatalf("config diagnostic = %q, want environment and redaction marker", diagnostic)
	}
}

func TestNewSDKConfigurationUsesSelectedEnvironmentAndSafeHTTPDefaults(t *testing.T) {
	tests := []struct {
		environment Environment
		wantURL     string
	}{
		{environment: EnvironmentSandbox, wantURL: string(plaidSDK.Sandbox)},
		{environment: EnvironmentProduction, wantURL: string(plaidSDK.Production)},
	}

	for _, test := range tests {
		t.Run(string(test.environment), func(t *testing.T) {
			config, err := NewConfig(fakeClientID, fakeSecret, test.environment)
			if err != nil {
				t.Fatalf("NewConfig() error: %v", err)
			}
			configuration, err := newSDKConfiguration(config, nil)
			if err != nil {
				t.Fatalf("newSDKConfiguration() error: %v", err)
			}

			serverURL, err := configuration.ServerURL(0, nil)
			if err != nil {
				t.Fatalf("ServerURL() error: %v", err)
			}
			if serverURL != test.wantURL {
				t.Errorf("server URL = %q, want %q", serverURL, test.wantURL)
			}
			if configuration.Debug {
				t.Error("Plaid SDK debug logging is enabled")
			}
			if configuration.HTTPClient.Timeout != defaultRequestTimeout {
				t.Errorf(
					"HTTP timeout = %s, want %s",
					configuration.HTTPClient.Timeout,
					defaultRequestTimeout,
				)
			}
			if configuration.DefaultHeader["PLAID-CLIENT-ID"] != fakeClientID {
				t.Error("Plaid client ID header was not configured")
			}
			if configuration.DefaultHeader["PLAID-SECRET"] != fakeSecret {
				t.Error("Plaid secret header was not configured")
			}
		})
	}
}

func TestNewSDKConfigurationPreservesInjectedHTTPClient(t *testing.T) {
	config, err := NewConfig(fakeClientID, fakeSecret, EnvironmentSandbox)
	if err != nil {
		t.Fatalf("NewConfig() error: %v", err)
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}

	configuration, err := newSDKConfiguration(config, httpClient)
	if err != nil {
		t.Fatalf("newSDKConfiguration() error: %v", err)
	}
	if configuration.HTTPClient != httpClient {
		t.Fatal("newSDKConfiguration() replaced injected HTTP client")
	}
}

func TestNewConfigRejectsCredentialWhitespaceWithoutExposure(t *testing.T) {
	for name, secret := range map[string]string{
		"surrounding": " " + fakeSecret,
		"embedded":    fakeSecret[:4] + "\n" + fakeSecret[4:],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewConfig(fakeClientID, secret, EnvironmentSandbox)
			if err == nil {
				t.Fatal("NewConfig() accepted credential whitespace")
			}
			if strings.Contains(err.Error(), fakeSecret) {
				t.Fatal("NewConfig() error exposes Plaid secret")
			}
		})
	}
}

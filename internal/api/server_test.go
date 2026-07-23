package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

const testAPIKey = "fake-test-api-key"

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name      string
		address   string
		allow     bool
		wantLocal bool
		wantErr   bool
	}{
		{"IPv4 loopback", "127.0.0.1:8080", false, true, false},
		{"IPv6 loopback", "[::1]:8080", false, true, false},
		{"localhost", "localhost:8080", false, true, false},
		{"ephemeral loopback", "127.0.0.1:0", false, true, false},
		{"unspecified IPv4 rejected", "0.0.0.0:8080", false, false, true},
		{"bare host rejected", ":8080", false, false, true},
		{"non-loopback hostname rejected", "example.test:8080", false, false, true},
		{"non-loopback explicit opt-in", "0.0.0.0:8080", true, false, false},
		{"bare host explicit opt-in", ":8080", true, false, false},
		{"missing port", "127.0.0.1", false, false, true},
		{"invalid port", "127.0.0.1:bad", false, false, true},
		{"out-of-range port", "127.0.0.1:70000", false, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			local, err := ValidateListenAddress(test.address, test.allow)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateListenAddress() error = %v, wantErr %v", err, test.wantErr)
			}
			if local != test.wantLocal {
				t.Errorf("ValidateListenAddress() local = %v, want %v", local, test.wantLocal)
			}
		})
	}
}

func openAPITestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "moneta.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db
}

func seedAPITestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	itemResult, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc, status, last_synced_at
		) VALUES ('plaid', 'item-fake', 'Fake Bank', ?, 'ok', '2026-07-22T12:00:00Z')
	`, []byte("encrypted-test-placeholder"))
	if err != nil {
		t.Fatalf("insert provider item: %v", err)
	}
	itemID, err := itemResult.LastInsertId()
	if err != nil {
		t.Fatalf("provider item id: %v", err)
	}
	insertAccount := func(name, accountType, providerID string) int64 {
		t.Helper()
		result, err := db.Exec(`
			INSERT INTO accounts (
				entity_id, provider_item_id, type, name, institution,
				provider, provider_account_id
			) VALUES (?, ?, ?, ?, 'Fake Bank', 'plaid', ?)
		`, entityID, itemID, accountType, name, providerID)
		if err != nil {
			t.Fatalf("insert account: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("account id: %v", err)
		}
		return id
	}
	checkingID := insertAccount("Everyday Checking", "checking", "acct-fake-1")
	creditID := insertAccount("Credit Example", "credit_card", "acct-fake-2")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-22', 120000), (?, '2026-07-22', 340000)
	`, checkingID, creditID); err != nil {
		t.Fatalf("insert balances: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15)
	`, creditID); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	insertTransaction := func(
		date string,
		amount int64,
		merchant string,
		category any,
		excluded int,
		hash string,
	) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, merchant_raw,
				merchant_norm, category_id, status, excluded, dedup_hash
			) VALUES (?, ?, ?, ?, ?, ?, ?, 'posted', ?, ?)
		`, checkingID, entityID, date, amount, merchant, merchant, category, excluded, hash); err != nil {
			t.Fatalf("insert transaction: %v", err)
		}
	}
	insertTransaction("2026-07-10", -2500, "Grocery Mart", int64(7), 0, "spend")
	insertTransaction("2026-07-10", 100000, "Employer Example", int64(1), 0, "income")
	insertTransaction("2026-07-10", -50000, "Transfer Example", int64(2), 1, "transfer")
	insertTransaction("2026-06-10", -1500, "Previous Grocery", int64(7), 0, "previous-spend")
}

func newTestHandler(t *testing.T, db *sql.DB, logger *log.Logger) http.Handler {
	t.Helper()
	handler, err := NewHandler(db, testAPIKey, logger)
	if err != nil {
		t.Fatalf("NewHandler() error: %v", err)
	}
	return handler
}

func performRequest(handler http.Handler, path, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if key != "" {
		request.Header.Set("X-API-Key", key)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAPIRequiresCorrectKeyOnEveryRoute(t *testing.T) {
	handler := newTestHandler(t, openAPITestDB(t), nil)
	routes := []string{
		"/v1/status",
		"/v1/accounts",
		"/v1/transactions",
		"/v1/spend?period=2026-07",
		"/v1/cashflow?period=2026-07",
		"/v1/networth",
		"/v1/debts",
		"/v1/trends?metric=mom&period=2026-07",
	}
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			for _, key := range []string{"", "wrong-key"} {
				response := performRequest(handler, route, key)
				if response.Code != http.StatusUnauthorized {
					t.Errorf("GET %s with key %q = %d, want 401", route, key, response.Code)
				}
				if response.Body.String() != "{\"error\":\"unauthorized\"}\n" {
					t.Errorf("unauthorized body = %q", response.Body.String())
				}
				if strings.Contains(response.Body.String(), testAPIKey) {
					t.Error("unauthorized response leaked API key")
				}
			}
		})
	}
}

func TestAPIReadRoutes(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	handler := newTestHandler(t, db, nil)
	tests := []struct {
		path  string
		wants []string
	}{
		{"/v1/status", []string{`"items":1`, `"institution":"Fake Bank"`}},
		{"/v1/accounts?type=checking", []string{`"accounts":1`, `"name":"Everyday Checking"`, `"balance":1200`}},
		{"/v1/transactions?from=2026-07-01&to=2026-07-31", []string{`"count":3`, `"excluded_count":1`, `"merchant":"Grocery Mart"`}},
		{"/v1/spend?period=2026-07", []string{`"total_spend":25`, `"category":"Food and Drink"`, `"merchant":"Grocery Mart"`}},
		{"/v1/cashflow?period=2026-07", []string{`"inflow":1000`, `"outflow":25`, `"net":975`, `"savings_rate":0.975`}},
		{"/v1/networth?as_of=2026-07-22", []string{`"assets":1200`, `"liabilities":3400`, `"networth":-2200`, `"type":"credit_card"`}},
		{"/v1/debts", []string{`"total_debt":3400`, `"name":"Credit Example"`, `"utilization":0.34`, `"apr":0.2299`}},
		{"/v1/trends?metric=mom&period=2026-07", []string{`"metric":"mom"`, `"spend_this":25`, `"spend_prev":15`, `"delta":10`, `"category":"Food and Drink"`}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(handler, test.path, testAPIKey)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %s", test.path, response.Code, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Errorf("Content-Type = %q", contentType)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			for _, want := range test.wants {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("GET %s missing %q: %s", test.path, want, response.Body.String())
				}
			}
		})
	}
}

func TestAPINetworthHistory(t *testing.T) {
	db := openAPITestDB(t)
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountResult, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'checking', 'History Checking', 'Fake Bank', 'plaid', 'history-checking')
	`, entityID)
	if err != nil {
		t.Fatalf("insert history account: %v", err)
	}
	accountID, err := accountResult.LastInsertId()
	if err != nil {
		t.Fatalf("history account id: %v", err)
	}
	fixedNow := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("local", -7*60*60))
	today := fixedNow.Format("2006-01-02")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, ?, 123400)
	`, accountID, today); err != nil {
		t.Fatalf("insert history balance: %v", err)
	}

	var logs bytes.Buffer
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(&logs, "", 0),
		now:        func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleNetworth)))
	response := performRequest(handler, "/v1/networth?history=1d", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET history = %d, want 200: %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		`"summary":{"from":"` + today + `","to":"` + today + `","days":1}`,
		`"history":[{"date":"` + today + `","assets":1234,"liabilities":0,"networth":1234}]`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("history response missing %q: %s", want, response.Body.String())
		}
	}
}

func TestAPIRecoversPanicsAndContinuesServing(t *testing.T) {
	var logs bytes.Buffer
	s := &server{
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(&logs, "", 0),
	}
	calls := 0
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				panic("fake handler panic")
			}
			writer.WriteHeader(http.StatusNoContent)
		},
	)))

	first := performRequest(handler, "/v1/test", testAPIKey)
	if first.Code != http.StatusInternalServerError ||
		first.Body.String() != "{\"error\":\"internal server error\"}\n" {
		t.Errorf("panic response = %d %q", first.Code, first.Body.String())
	}
	if !strings.Contains(logs.String(), "REST handler panic: fake handler panic") {
		t.Errorf("panic log = %q", logs.String())
	}
	if strings.Contains(logs.String(), testAPIKey) {
		t.Error("panic log leaked API key")
	}

	second := performRequest(handler, "/v1/test", testAPIKey)
	if second.Code != http.StatusNoContent {
		t.Errorf("request after panic = %d, want 204", second.Code)
	}
}

func TestAPIReturnsJSONForUnknownPathsAndMethods(t *testing.T) {
	handler := newTestHandler(t, openAPITestDB(t), nil)
	tests := []struct {
		method    string
		path      string
		code      int
		body      string
		wantAllow string
	}{
		{http.MethodGet, "/v1/unknown", http.StatusNotFound, "{\"error\":\"not found\"}\n", ""},
		{http.MethodPost, "/v1/status", http.StatusMethodNotAllowed, "{\"error\":\"method not allowed\"}\n", "GET, HEAD"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("X-API-Key", testAPIKey)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.code || response.Body.String() != test.body {
			t.Errorf("%s %s = %d %q, want %d %q",
				test.method, test.path, response.Code, response.Body.String(), test.code, test.body)
		}
		if response.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s %s Content-Type = %q", test.method, test.path, response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Allow") != test.wantAllow {
			t.Errorf("%s %s Allow = %q, want %q",
				test.method, test.path, response.Header().Get("Allow"), test.wantAllow)
		}
	}
}

func TestAPIRejectsInvalidQueries(t *testing.T) {
	handler := newTestHandler(t, openAPITestDB(t), nil)
	tests := []struct {
		path string
		want string
	}{
		{"/v1/status?limit=0", "at least 1"},
		{"/v1/accounts?type=bogus", "unknown account type"},
		{"/v1/transactions?from=2026-02-30", "valid YYYY-MM-DD"},
		{"/v1/spend?period=2026-07&from=2026-07-01&to=2026-07-31", "cannot be combined"},
		{"/v1/cashflow?from=2026-07-01", "must be provided together"},
		{"/v1/networth?as_of=bad", "valid YYYY-MM-DD"},
		{"/v1/networth?history=week", "must use Nd form"},
		{"/v1/networth?history=", "must use Nd form"},
		{"/v1/networth?history=7d&as_of=2026-07-22", "cannot be combined"},
		{"/v1/networth?unexpected=value", "unknown query parameter"},
		{"/v1/debts?unexpected=value", "unknown query parameter"},
		{"/v1/trends", "metric"},
		{"/v1/trends?metric=merchants", "unknown metric"},
		{"/v1/trends?metric=mom&period=2026-13", "valid YYYY-MM"},
		{"/v1/trends?metric=mom&from=2026-07-01&to=2026-07-31", "requires period"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(handler, test.path, testAPIKey)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400: %s", test.path, response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.want) {
				t.Errorf("GET %s body = %q, want %q", test.path, response.Body.String(), test.want)
			}
			if strings.Contains(response.Body.String(), testAPIKey) {
				t.Error("bad-query response leaked API key")
			}
		})
	}
}

func TestAPIInternalErrorDoesNotLeakKey(t *testing.T) {
	db := openAPITestDB(t)
	var logs bytes.Buffer
	handler := newTestHandler(t, db, log.New(&logs, "", 0))
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	response := performRequest(handler, "/v1/status", testAPIKey)
	if response.Code != http.StatusInternalServerError ||
		response.Body.String() != "{\"error\":\"internal server error\"}\n" {
		t.Errorf("response = %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), testAPIKey) || strings.Contains(logs.String(), testAPIKey) {
		t.Error("internal error leaked API key")
	}
}

func TestNewHandlerValidatesConfiguration(t *testing.T) {
	db := openAPITestDB(t)
	if _, err := NewHandler(nil, testAPIKey, nil); err == nil {
		t.Error("NewHandler(nil db) succeeded")
	}
	if _, err := NewHandler(db, "", nil); err == nil {
		t.Error("NewHandler(empty key) succeeded")
	}
}

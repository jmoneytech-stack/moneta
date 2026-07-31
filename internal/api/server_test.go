package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/report"
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
	insertTransaction("2026-07-10", -2000, "Grocery Mart", int64(7), 0, "spend")
	insertTransaction("2026-07-11", -500, "Cafe Example", int64(7), 0, "cafe")
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
		"/v1/cards",
		"/v1/recurring",
		"/v1/bills",
		"/v1/anomalies",
		"/v1/dashboard",
		"/v1/trends?metric=mom&period=2026-07",
		"/v1/trends?metric=merchants&period=2026-07",
		"/v1/trends?metric=utilization&history=1d",
		"/v1/trends?metric=savings&period=2026-07",
		"/v1/trends?metric=fixed-variable&period=2026-07",
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
		{"/v1/transactions?from=2026-07-01&to=2026-07-31", []string{`"count":4`, `"excluded_count":1`, `"merchant":"Grocery Mart"`}},
		{"/v1/spend?period=2026-07", []string{`"total_spend":25`, `"category":"Food and Drink"`, `"merchant":"Grocery Mart"`}},
		{"/v1/cashflow?period=2026-07", []string{`"inflow":1000`, `"outflow":25`, `"net":975`, `"savings_rate":0.975`}},
		{"/v1/networth?as_of=2026-07-22", []string{`"assets":1200`, `"liabilities":3400`, `"networth":-2200`, `"type":"credit_card"`}},
		{"/v1/debts", []string{`"total_debt":3400`, `"name":"Credit Example"`, `"utilization":0.34`, `"apr":0.2299`}},
		{"/v1/cards", []string{`"count":1`, `"total_debt":3400`, `"missing_balance":0`, `"name":"Credit Example"`, `"limit":10000`, `"utilization":0.34`, `"apr":0.2299`, `"due_day":15`}},
		{"/v1/recurring", []string{`"detector":{"status":"never_run"`, `"recurring":[]`}},
		{"/v1/bills", []string{`"detector":{"status":"never_run"`, `"count":1`, `"name":"Credit Example"`, `"source":"card_due"`}},
		{"/v1/anomalies", []string{`"count":0`, `"skipped_overflow":0`, `"anomalies":[]`}},
		{"/v1/trends?metric=mom&period=2026-07", []string{`"metric":"mom"`, `"spend_this":25`, `"spend_prev":15`, `"delta":10`, `"category":"Food and Drink"`}},
		{"/v1/trends?metric=merchants&period=2026-07", []string{`"metric":"merchants"`, `"spend":25`, `"count":2`, `"merchants":2`, `"merchant":"Grocery Mart"`, `"merchant":"Cafe Example"`}},
		{"/v1/trends?metric=merchants&from=2026-07-01&to=2026-07-31", []string{`"metric":"merchants"`, `"from":"2026-07-01"`, `"to":"2026-07-31"`, `"spend":25`, `"count":2`}},
		{"/v1/trends?metric=utilization&from=2026-07-22&to=2026-07-22", []string{`"metric":"utilization"`, `"days":1`, `"accounts":1`, `"missing_limit_days":0`, `"date":"2026-07-22"`, `"utilization":0.34`, `"debt":3400`, `"limit":10000`}},
		{"/v1/trends?metric=utilization&period=2026-07", []string{`"metric":"utilization"`, `"from":"2026-07-01"`, `"to":"2026-07-31"`, `"days":31`, `"utilization":0.34`}},
		{"/v1/trends?metric=savings&period=2026-07", []string{`"metric":"savings"`, `"count":3`, `"inflow":1000`, `"outflow":25`, `"net":975`, `"savings_rate":0.975`}},
		{"/v1/trends?metric=fixed-variable&period=2026-07", []string{`"metric":"fixed-variable"`, `"fixed":0`, `"variable":25`, `"unclassified":0`, `"total":25`, `"fixed_share":0`}},
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

func TestAPIStatusIncludesDetectorState(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	if err := store.UpsertDetectorState(context.Background(), db, store.DetectorState{
		Status:              "error",
		LastRunAt:           "2026-08-15T12:00:00.000Z",
		LastSuccessAt:       "2026-08-01T12:00:00.000Z",
		LastError:           "recurring detection failed",
		LastSkippedOverflow: 3,
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	handler := newTestHandler(t, db, nil)
	response := performRequest(handler, "/v1/status", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/status = %d, want 200: %s", response.Code, response.Body.String())
	}
	want := `"detector":{"status":"error","last_run_at":"2026-08-15T12:00:00.000Z","last_success_at":"2026-08-01T12:00:00.000Z","last_skipped_overflow":3,"last_error":"recurring detection failed"}`
	if !strings.Contains(response.Body.String(), want) {
		t.Errorf("status response missing detector state %q: %s", want, response.Body.String())
	}
}

func TestAPIRecurringMirrorsCLI(t *testing.T) {
	db := openAPITestDB(t)
	ctx := context.Background()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if err := store.UpsertDetectorState(ctx, db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-08-15T12:00:00.000Z",
		LastSuccessAt: "2026-08-15T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, last_matched_cents,
			schedule_anchor_day
		) VALUES (?, 'Streambox Example', 'subscription', 'monthly', -10000,
			'2026-09-01', 'detected', 1, 'streambox example', -1, -12000, 1)
	`, entityID); err != nil {
		t.Fatalf("seed API recurring row: %v", err)
	}
	fixedNow := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(io.Discard, "", 0),
		now:        func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleRecurring)))
	response := performRequest(handler, "/v1/recurring", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/recurring = %d, want 200: %s", response.Code, response.Body.String())
	}
	storeReport, err := store.ReadRecurring(ctx, db, store.RecurringFilter{AsOf: "2026-08-15"})
	if err != nil {
		t.Fatalf("ReadRecurring() expected document: %v", err)
	}
	var expected bytes.Buffer
	if err := cli.Render(&expected, report.Recurring(storeReport), cli.FormatJSON); err != nil {
		t.Fatalf("render expected recurring JSON: %v", err)
	}
	if response.Body.String() != expected.String() {
		t.Errorf("API recurring != shared CLI document:\nAPI: %s\nCLI: %s",
			response.Body.String(), expected.String())
	}

	for _, path := range []string{
		"/v1/recurring?unknown=1",
		"/v1/recurring?kind=other",
	} {
		response := performRequest(handler, path, testAPIKey)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", path, response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, "/v1/recurring", "wrong-key"); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized recurring status = %d, want 401", response.Code)
	}
}

func TestAPIBillsMirrorsCLI(t *testing.T) {
	db := openAPITestDB(t)
	ctx := context.Background()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if err := store.UpsertDetectorState(ctx, db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-07-01T12:00:00.000Z",
		LastSuccessAt: "2026-07-01T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, schedule_anchor_day
		) VALUES (?, 'API Bill Example', 'bill', 'monthly', -2400,
			'2026-07-10', 'detected', 1, 'api bill example', -1, 10)
	`, entityID); err != nil {
		t.Fatalf("seed API bill: %v", err)
	}
	fixedNow := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	s := &server{
		db: db, apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger: log.New(io.Discard, "", 0), now: func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleBills)))
	response := performRequest(handler, "/v1/bills?days=30", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/bills = %d, want 200: %s", response.Code, response.Body.String())
	}
	storeReport, err := store.ReadBills(ctx, db, "2026-07-01", 30)
	if err != nil {
		t.Fatalf("ReadBills() expected document: %v", err)
	}
	var expected bytes.Buffer
	if err := cli.Render(&expected, report.Bills(storeReport), cli.FormatJSON); err != nil {
		t.Fatalf("render expected bills JSON: %v", err)
	}
	if response.Body.String() != expected.String() {
		t.Errorf("API bills != shared CLI document:\nAPI: %s\nCLI: %s",
			response.Body.String(), expected.String())
	}
	for _, path := range []string{
		"/v1/bills?days=0", "/v1/bills?days=367", "/v1/bills?days=bad",
		"/v1/bills?days=30&days=31", "/v1/bills?unknown=1",
	} {
		response := performRequest(handler, path, testAPIKey)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", path, response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, "/v1/bills", "wrong-key"); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized bills status = %d, want 401", response.Code)
	}
}

func TestAPIAnomaliesMirrorsCLI(t *testing.T) {
	db := openAPITestDB(t)
	ctx := context.Background()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	accountResult, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, provider, provider_account_id
		) VALUES (?, 'checking', 'Anomaly Checking', 'plaid', 'api-anomaly-checking')
	`, entityID)
	if err != nil {
		t.Fatalf("seed anomaly account: %v", err)
	}
	accountID, err := accountResult.LastInsertId()
	if err != nil {
		t.Fatalf("anomaly account id: %v", err)
	}
	categoryResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('API Anomaly Example', 'expense')
	`)
	if err != nil {
		t.Fatalf("seed anomaly category: %v", err)
	}
	categoryID, err := categoryResult.LastInsertId()
	if err != nil {
		t.Fatalf("anomaly category id: %v", err)
	}
	for index, fixture := range []struct {
		date   string
		amount int64
	}{
		{"2026-01-15", -10000}, {"2026-02-15", -10000},
		{"2026-03-15", -10000}, {"2026-04-15", -31000},
	} {
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, category_id,
				status, excluded, is_transfer, dedup_hash
			) VALUES (?, ?, ?, ?, ?, 'posted', 0, 0, ?)
		`, accountID, entityID, fixture.date, fixture.amount, categoryID,
			fmt.Sprintf("api-anomaly-%d", index)); err != nil {
			t.Fatalf("seed anomaly transaction: %v", err)
		}
	}
	fixedNow := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC)
	s := &server{
		db: db, apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger: log.New(io.Discard, "", 0), now: func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleAnomalies)))
	response := performRequest(handler, "/v1/anomalies?period=2026-04", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/anomalies = %d, want 200: %s", response.Code, response.Body.String())
	}
	storeReport, err := store.ReadAnomalies(ctx, db, "2026-05-20", "2026-04")
	if err != nil {
		t.Fatalf("ReadAnomalies() expected document: %v", err)
	}
	var expected bytes.Buffer
	if err := cli.Render(&expected, report.Anomalies(storeReport), cli.FormatJSON); err != nil {
		t.Fatalf("render expected anomalies JSON: %v", err)
	}
	if response.Body.String() != expected.String() {
		t.Errorf("API anomalies != shared CLI document:\nAPI: %s\nCLI: %s",
			response.Body.String(), expected.String())
	}
	for _, path := range []string{
		"/v1/anomalies?period=2026-06", "/v1/anomalies?period=2026-13",
		"/v1/anomalies?period=2026-04&period=2026-03", "/v1/anomalies?unknown=1",
	} {
		response := performRequest(handler, path, testAPIKey)
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400: %s", path, response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, "/v1/anomalies", "wrong-key"); response.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized anomalies status = %d, want 401", response.Code)
	}
}

func TestAPIDashboardComposesSectionsWithPlaceholders(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	fixedNow := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	var logs bytes.Buffer
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(&logs, "", 0),
		now:        func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleDashboard)))
	response := performRequest(handler, "/v1/dashboard", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/dashboard = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`"summary":{"as_of":"2026-07-22"}`,
		`"networth":{"assets":1200,"liabilities":3400,"networth":-2200,"accounts":2,"missing_balance":0}`,
		`"cash":{"balance":1200,"accounts":1,"note":"checking + savings latest balances"}`,
		`"credit":{"utilization":0.34,"total_debt":3400,"cards":1}`,
		`"spend_month":{"from":"2026-07-01","to":"2026-07-31","total":25,"count":2}`,
		`"cashflow_month":{"inflow":1000,"outflow":25,"net":975,"savings_rate":0.975,"count":3}`,
		`"sync":{"items":1,"needs_attention":0,"login_required":0}`,
		`"upcoming_bills":null`,
		`"anomalies":null`,
		`"phase4_note":"anomalies are available in a later phase"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard response missing %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{`"upcoming_bills":0`, `"anomalies":0`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("dashboard fabricates a Phase 4 value %q: %s", unwanted, body)
		}
	}
}

func TestAPIDashboardSurfacesLoginRequired(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	if _, err := db.Exec(`
		UPDATE provider_items SET status = 'login_required' WHERE item_id = 'item-fake'
	`); err != nil {
		t.Fatalf("set login_required: %v", err)
	}
	fixedNow := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	var logs bytes.Buffer
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(&logs, "", 0),
		now:        func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleDashboard)))
	response := performRequest(handler, "/v1/dashboard", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/dashboard = %d, want 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"sync":{"items":1,"needs_attention":1,"login_required":1}`) {
		t.Errorf("dashboard did not surface login_required: %s", response.Body.String())
	}
}

func TestAPICardsExcludesLoansAndNullLimitUtilization(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	entityID, err := store.EnsureDefaultEntity(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	insertAccount := func(name, accountType, providerID string) int64 {
		t.Helper()
		result, err := db.Exec(`
			INSERT INTO accounts (
				entity_id, type, name, institution, provider, provider_account_id
			) VALUES (?, ?, ?, 'Fake Bank', 'plaid', ?)
		`, entityID, accountType, name, providerID)
		if err != nil {
			t.Fatalf("insert account: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("account id: %v", err)
		}
		return id
	}
	loan := insertAccount("Auto Loan", "loan", "acct-loan-1")
	noLimit := insertAccount("Store Card", "credit_card", "acct-card-2")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-22', 500000), (?, '2026-07-22', 15000)
	`, loan, noLimit); err != nil {
		t.Fatalf("insert balances: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, NULL, 27.49, 3)
	`, noLimit); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}

	handler := newTestHandler(t, db, nil)
	response := performRequest(handler, "/v1/cards", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/cards = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`"summary":{"count":2,"total_debt":3550,"missing_balance":0}`,
		`{"name":"Credit Example","balance":3400,"limit":10000,"utilization":0.34,"apr":0.2299,"due_day":15}`,
		`{"name":"Store Card","balance":150,"limit":null,"utilization":null,"apr":0.2749,"due_day":3}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cards response missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Auto Loan") {
		t.Errorf("cards response includes a loan: %s", body)
	}

	debts := performRequest(handler, "/v1/debts", testAPIKey)
	if debts.Code != http.StatusOK || !strings.Contains(debts.Body.String(), "Auto Loan") {
		t.Errorf("GET /v1/debts = %d, want 200 including the loan: %s", debts.Code, debts.Body.String())
	}
}

func TestAPITrendFixedVariableReturnsExactBuckets(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	var accountID, entityID int64
	if err := db.QueryRow(`
		SELECT id, entity_id FROM accounts WHERE provider_account_id = 'acct-fake-1'
	`).Scan(&accountID, &entityID); err != nil {
		t.Fatalf("read fixed-variable fixture account: %v", err)
	}
	insert := func(amount int64, merchant string, category any, hash string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, merchant_raw,
				merchant_norm, category_id, status, excluded, dedup_hash
			) VALUES (?, ?, '2026-07-20', ?, ?, ?, ?, 'posted', 0, ?)
		`, accountID, entityID, amount, merchant, merchant, category, hash); err != nil {
			t.Fatalf("insert fixed-variable transaction %q: %v", hash, err)
		}
	}
	insert(-5000, "Rent Example", int64(16), "api-fixed-rent")
	insert(-2000, "Unknown Example", nil, "api-fixed-unclassified")

	handler := newTestHandler(t, db, nil)
	response := performRequest(
		handler,
		"/v1/trends?metric=fixed-variable&from=2026-07-01&to=2026-07-31",
		testAPIKey,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("GET fixed-variable = %d, want 200: %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		`"summary":{"metric":"fixed-variable","from":"2026-07-01","to":"2026-07-31","fixed":50,"variable":25,"unclassified":20,"total":95,"fixed_share":0.5263}`,
		`"by_bucket":[{"bucket":"fixed","spend":50,"count":1},{"bucket":"variable","spend":25,"count":2},{"bucket":"unclassified","spend":20,"count":1}]`,
		`"hint":"heuristic: fixed = Rent and Utilities only; not recurring-based"`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("fixed-variable response missing %q: %s", want, response.Body.String())
		}
	}
}

func TestAPITrendSavingsMatchesCashflow(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	handler := newTestHandler(t, db, nil)
	cashflow := performRequest(handler, "/v1/cashflow?period=2026-07", testAPIKey)
	savings := performRequest(handler, "/v1/trends?metric=savings&period=2026-07", testAPIKey)
	if cashflow.Code != http.StatusOK || savings.Code != http.StatusOK {
		t.Fatalf("cashflow/savings status = %d/%d, want 200/200", cashflow.Code, savings.Code)
	}
	for _, field := range []string{
		`"count":3`,
		`"inflow":1000`,
		`"outflow":25`,
		`"net":975`,
		`"savings_rate":0.975`,
	} {
		if !strings.Contains(cashflow.Body.String(), field) ||
			!strings.Contains(savings.Body.String(), field) {
			t.Errorf("cashflow/savings parity missing %q:\ncashflow=%s\nsavings=%s",
				field, cashflow.Body.String(), savings.Body.String())
		}
	}
}

func TestAPITrendUtilizationHistory(t *testing.T) {
	db := openAPITestDB(t)
	seedAPITestDB(t, db)
	fixedNow := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("local", -7*60*60))
	var logs bytes.Buffer
	s := &server{
		db:         db,
		apiKeyHash: sha256.Sum256([]byte(testAPIKey)),
		logger:     log.New(&logs, "", 0),
		now:        func() time.Time { return fixedNow },
	}
	handler := s.authenticate(s.recoverPanics(http.HandlerFunc(s.handleTrends)))
	response := performRequest(handler, "/v1/trends?metric=utilization&history=1d", testAPIKey)
	if response.Code != http.StatusOK {
		t.Fatalf("GET utilization history = %d, want 200: %s", response.Code, response.Body.String())
	}
	for _, want := range []string{
		`"summary":{"metric":"utilization","from":"2026-07-22","to":"2026-07-22","days":1,"accounts":1,"missing_limit_days":0}`,
		`"history":[{"date":"2026-07-22","utilization":0.34,"debt":3400,"limit":10000,"accounts":1}]`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("utilization history response missing %q: %s", want, response.Body.String())
		}
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
		{http.MethodPost, "/v1/recurring", http.StatusMethodNotAllowed, "{\"error\":\"method not allowed\"}\n", "GET, HEAD"},
		{http.MethodPost, "/v1/bills", http.StatusMethodNotAllowed, "{\"error\":\"method not allowed\"}\n", "GET, HEAD"},
		{http.MethodPost, "/v1/anomalies", http.StatusMethodNotAllowed, "{\"error\":\"method not allowed\"}\n", "GET, HEAD"},
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
		{"/v1/cards?unexpected=value", "unknown query parameter"},
		{"/v1/recurring?kind=other", "kind must be"},
		{"/v1/recurring?unexpected=value", "unknown query parameter"},
		{"/v1/bills?days=0", "integer from 1 to 366"},
		{"/v1/bills?unexpected=value", "unknown query parameter"},
		{"/v1/anomalies?period=9999-12", "must not be in the future"},
		{"/v1/anomalies?unexpected=value", "unknown query parameter"},
		{"/v1/dashboard?unexpected=value", "unknown query parameter"},
		{"/v1/trends", "metric"},
		{"/v1/trends?metric=cards", "unknown metric"},
		{"/v1/trends?metric=mom&period=2026-13", "valid YYYY-MM"},
		{"/v1/trends?metric=mom&from=2026-07-01&to=2026-07-31", "requires period"},
		{"/v1/trends?metric=merchants&period=2026-13", "valid YYYY-MM"},
		{"/v1/trends?metric=merchants&period=2026-07&from=2026-07-01&to=2026-07-31", "cannot be combined"},
		{"/v1/trends?metric=merchants&from=2026-07-01", "must be provided together"},
		{"/v1/trends?metric=mom&history=30d", "history is supported only"},
		{"/v1/trends?metric=merchants&history=30d", "history is supported only"},
		{"/v1/trends?metric=utilization&history=0d", "at least 1 day"},
		{"/v1/trends?metric=utilization&history=30d&period=2026-07", "cannot be combined"},
		{"/v1/trends?metric=utilization&limit=5", "limit/full are unsupported"},
		{"/v1/trends?metric=savings&history=30d", "history is supported only"},
		{"/v1/trends?metric=savings&full=true", "limit/full are unsupported"},
		{"/v1/trends?metric=savings&period=2026-07&from=2026-07-01&to=2026-07-31", "cannot be combined"},
		{"/v1/trends?metric=fixed-variable&history=30d", "history is supported only"},
		{"/v1/trends?metric=fixed-variable&limit=3", "limit/full are unsupported"},
		{"/v1/trends?metric=fixed-variable&full=true", "limit/full are unsupported"},
		{"/v1/trends?metric=fixed-variable&period=2026-07&from=2026-07-01&to=2026-07-31", "cannot be combined"},
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

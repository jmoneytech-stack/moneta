package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

func seedDashboardCommandDB(t *testing.T, itemStatus string) string {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO provider_items (
			provider, item_id, institution, access_token_enc, status, last_synced_at
		) VALUES ('plaid', 'item-fake', 'Fake Bank', ?, ?, '2026-07-22T12:00:00Z')
	`, []byte("encrypted-test-placeholder"), itemStatus); err != nil {
		t.Fatalf("insert provider item: %v", err)
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
	checking := insertAccount("Everyday Checking", "checking", "chk-1")
	savings := insertAccount("Rainy Day", "savings", "sav-1")
	travel := insertAccount("Travel Card", "credit_card", "card-1")
	storeCard := insertAccount("Store Card", "credit_card", "card-2")
	loan := insertAccount("Auto Loan", "loan", "loan-1")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-22', 120000), (?, '2026-07-22', 50000),
		       (?, '2026-07-22', 340000), (?, '2026-07-22', 15000),
		       (?, '2026-07-22', 500000)
	`, checking, savings, travel, storeCard, loan); err != nil {
		t.Fatalf("insert balances: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, NULL, 27.49, 3)
	`, travel, storeCard); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	insertTransaction := func(date string, amount int64, merchant, hash string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, merchant_raw,
				merchant_norm, category_id, status, excluded, dedup_hash
			) VALUES (?, ?, ?, ?, ?, ?, 7, 'posted', 0, ?)
		`, checking, entityID, date, amount, merchant, merchant, hash); err != nil {
			t.Fatalf("insert transaction %q: %v", hash, err)
		}
	}
	insertTransaction("2026-07-10", -2500, "Coffee Example", "dash-spend")
	insertTransaction("2026-07-01", 100000, "Payroll Example", "dash-inflow")
	return databasePath
}

// TestDashboardComposesSections is the PR10 acceptance test from
// Every dashboard section is populated from its underlying store read. The
// detector gate keeps upcoming bills null before a successful detect, while
// anomalies always reports the previous complete month.
func TestDashboardComposesSections(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDashboardCommandDB(t, "ok"))
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runDashboardAt(context.Background(), nil, &stdout, &stderr, now)
	if code != 0 {
		t.Fatalf("runDashboardAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"as_of: 2026-07-22",
		// networth, composed from ReadNetworth
		"assets: 1700",
		"liabilities: 8550",
		"networth: -6850",
		// cash: checking + savings only
		"balance: 1700",
		"checking + savings latest balances",
		// credit: cards portfolio, 3400/10000
		"utilization: 0.34",
		"total_debt: 3550",
		"cards: 2",
		// month spend and cashflow
		"from: 2026-07-01",
		"to: 2026-07-31",
		"total: 25",
		"inflow: 1000",
		"outflow: 25",
		"net: 975",
		"savings_rate: 0.975",
		// sync health
		"items: 1",
		"needs_attention: 0",
		"login_required: 0",
		// Detector-gated bills and always-real anomaly projection.
		"upcoming_bills: null",
		"anomalies:",
		"period: 2026-06",
		"top[0]{category,spend,baseline,deviation_ratio}:",
		"skipped_overflow: 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDashboardJSONIncludesAnomaliesObject(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDashboardCommandDB(t, "ok"))
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runDashboardAt(context.Background(), []string{"--json"}, &stdout, &stderr, now)
	if code != 0 {
		t.Fatalf("runDashboardAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	for _, want := range []string{
		`"summary":{"as_of":"2026-07-22"}`,
		`"networth":{"assets":1700,"liabilities":8550,"networth":-6850,"accounts":5,"missing_balance":0}`,
		`"cash":{"balance":1700,"accounts":2,"note":"checking + savings latest balances"}`,
		`"credit":{"utilization":0.34,"total_debt":3550,"cards":2}`,
		`"spend_month":{"from":"2026-07-01","to":"2026-07-31","total":25,"count":1}`,
		`"cashflow_month":{"inflow":1000,"outflow":25,"net":975,"savings_rate":0.975,"count":2}`,
		`"sync":{"items":1,"needs_attention":0,"login_required":0}`,
		`"upcoming_bills":null`,
		`"anomalies":{"period":"2026-06","count":0,"top":[],"skipped_overflow":0}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard JSON missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		`"upcoming_bills":0`, `"upcoming_bills":[]`, `"anomalies":null`, `"phase4_note"`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("dashboard JSON contains obsolete/dishonest value %q:\n%s", unwanted, out)
		}
	}
}

func TestRunDashboardExitsThreeOnLoginRequired(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDashboardCommandDB(t, "login_required"))
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runDashboardAt(context.Background(), nil, &stdout, &stderr, now)
	if code != 3 {
		t.Fatalf("runDashboardAt() code = %d, want 3 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	// The document still renders in full so an agent can read the payload.
	for _, want := range []string{
		"needs_attention: 1",
		"login_required: 1",
		"networth: -6850",
		"moneta link",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("login_required dashboard missing %q:\n%s", want, out)
		}
	}
}

func TestRunDashboardEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	code := runDashboardAt(context.Background(), nil, &stdout, &stderr, now)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"as_of: null",
		"networth: 0",
		"utilization: null",
		"cards: 0",
		"savings_rate: null",
		"items: 0",
		"upcoming_bills: null",
		"anomalies:",
		"period: 2026-06",
		"top[0]{category,spend,baseline,deviation_ratio}:",
		"skipped_overflow: 0",
		"moneta link",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty dashboard missing %q:\n%s", want, out)
		}
	}
}

func TestRunDashboardUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"dashboard", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"missing db", []string{"dashboard"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"dashboard", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != 2 {
				t.Errorf("run() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantText)
			}
		})
	}
}

// R3(b)/B1: the dashboard is an explicit subcommand. Bare moneta keeps
// printing usage and exiting 2.
func TestBareMonetaStaysUsageExitTwo(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDashboardCommandDB(t, "ok"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run(no args) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: moneta <command>") {
		t.Errorf("bare moneta did not print usage:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "upcoming_bills") {
		t.Errorf("bare moneta rendered a dashboard:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "  dashboard ") {
		t.Errorf("usage does not list dashboard:\n%s", stderr.String())
	}
}

func TestDashboardCashIgnoresNonDepositoryAssets(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	insert := func(name, accountType, providerID string, cents int64) {
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
		if _, err := db.Exec(`
			INSERT INTO balance_snapshots (account_id, date, current_cents)
			VALUES (?, '2026-07-22', ?)
		`, id, cents); err != nil {
			t.Fatalf("insert balance: %v", err)
		}
	}
	insert("Everyday Checking", "checking", "chk-1", 120000)
	insert("Brokerage", "investment", "inv-1", 900000)
	insert("Condo", "asset", "asset-1", 5000000)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	t.Setenv(databasePathEnvironment, databasePath)
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"dashboard", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"cash":{"balance":1200,"accounts":1`) {
		t.Errorf("cash should count depository accounts only:\n%s", out)
	}
	if !strings.Contains(out, `"assets":60200`) {
		t.Errorf("networth assets should still include investments and assets:\n%s", out)
	}
}

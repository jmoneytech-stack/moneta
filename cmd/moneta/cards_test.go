package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

func seedCardsCommandDB(t *testing.T) string {
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
	card := insertAccount("Travel Card", "credit_card", "card-1")
	noLimit := insertAccount("Store Card", "credit_card", "card-2")
	loan := insertAccount("Auto Loan", "loan", "loan-1")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-22', 340000), (?, '2026-07-22', 15000), (?, '2026-07-22', 500000)
	`, card, noLimit, loan); err != nil {
		t.Fatalf("insert balances: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, NULL, 27.49, 3)
	`, card, noLimit); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO loan_terms (account_id, apr) VALUES (?, 5.5)
	`, loan); err != nil {
		t.Fatalf("insert loan terms: %v", err)
	}
	return databasePath
}

// TestCardsShowsUtilizationAndDueDates is the PR9 acceptance test from
// docs/phase3-analytics-plan.md: a real limit yields utilization, a NULL limit
// yields null (never 0%), APR renders as basis points, and loans stay out.
func TestCardsShowsUtilizationAndDueDates(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedCardsCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cards"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 2",
		"total_debt: 3550",
		"missing_balance: 0",
		"cards[2]{name,balance,limit,utilization,apr,due_day}:",
		"Store Card,150,null,null,0.2749,3",
		"Travel Card,3400,10000,0.34,0.2299,15",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cards output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Auto Loan", "5000"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("cards output should not contain loan data %q:\n%s", unwanted, out)
		}
	}
}

func TestRunCardsJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedCardsCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cards", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	want := `{"summary":{"count":2,"total_debt":3550,"missing_balance":0}`
	if !strings.HasPrefix(out, want) {
		t.Errorf("cards --json output = %q, want prefix %q", out, want)
	}
	for _, fragment := range []string{
		`{"name":"Travel Card","balance":3400,"limit":10000,"utilization":0.34,"apr":0.2299,"due_day":15}`,
		`{"name":"Store Card","balance":150,"limit":null,"utilization":null,"apr":0.2749,"due_day":3}`,
	} {
		if !strings.Contains(out, fragment) {
			t.Errorf("cards --json missing %q: %q", fragment, out)
		}
	}
}

func TestRunCardsEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cards"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 0",
		"total_debt: 0",
		"missing_balance: 0",
		"cards[0]{name,balance,limit,utilization,apr,due_day}:",
		"no credit-card accounts yet; run moneta sync",
		"moneta debts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cards empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCardsMissingBalanceHint(t *testing.T) {
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
	if _, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'credit_card', 'Pending Card', 'Fake Bank', 'plaid', 'card-1')
	`, entityID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	t.Setenv(databasePathEnvironment, databasePath)
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"cards"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"missing_balance: 1",
		"Pending Card,null,null,null,null,null",
		"run moneta sync to pull balances",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cards missing-balance output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCardsUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"cards", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"missing db", []string{"cards"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"cards", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
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

func TestUsageListsCardsCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(help) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "  cards ") {
		t.Errorf("usage does not list cards:\n%s", stdout.String())
	}
}

// Loans must keep showing up under moneta debts after the shared store query
// gained a card-only scope.
func TestRunDebtsStillIncludesLoansAfterCards(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedCardsCommandDB(t))
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"debts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 3",
		"total_debt: 8550",
		"Auto Loan,loan,5000,null,null,0.055,null",
		"Travel Card,credit_card,3400,10000,0.34,0.2299,15",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("debts output missing %q:\n%s", want, out)
		}
	}
}

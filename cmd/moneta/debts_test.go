package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

func seedDebtsCommandDB(t *testing.T) string {
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
	loan := insertAccount("Auto Loan", "loan", "loan-1")
	missing := insertAccount("Credit Missing", "credit_card", "card-2")
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-22', 340000), (?, '2026-07-22', 500000)
	`, card, loan); err != nil {
		t.Fatalf("insert balances: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, 0, NULL, NULL)
	`, card, missing); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO loan_terms (account_id, apr)
		VALUES (?, 5.5)
	`, loan); err != nil {
		t.Fatalf("insert loan terms: %v", err)
	}
	return databasePath
}

func TestRunDebtsEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"debts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 0",
		"total_debt: 0",
		"missing_balance: 0",
		"debts[0]{name,type,balance,limit,utilization,apr,due_day}:",
		"no credit-card or loan accounts yet; run moneta sync",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("debts empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDebtsRendersBalancesTermsAndUtilization(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDebtsCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"debts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 3",
		"total_debt: 8400",
		"missing_balance: 1",
		"debts[3]{name,type,balance,limit,utilization,apr,due_day}:",
		"Auto Loan,loan,5000,null,null,0.055,null",
		"Credit Missing,credit_card,null,0,null,null,null",
		"Travel Card,credit_card,3400,10000,0.34,0.2299,15",
		"run moneta sync to pull balances",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("debts output missing %q:\n%s", want, out)
		}
	}
}

func TestRunDebtsJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedDebtsCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"debts", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	want := `{"summary":{"count":3,"total_debt":8400,"missing_balance":1}`
	if !strings.HasPrefix(out, want) {
		t.Errorf("debts --json output = %q, want prefix %q", out, want)
	}
	if !strings.Contains(out, `{"name":"Travel Card","type":"credit_card","balance":3400,"limit":10000,"utilization":0.34,"apr":0.2299,"due_day":15}`) {
		t.Errorf("debts --json missing card row: %q", out)
	}
}

func TestRunDebtsUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"debts", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"missing db", []string{"debts"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"debts", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
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

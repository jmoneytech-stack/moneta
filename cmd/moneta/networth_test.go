package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

func seedNetworthCommandDB(t *testing.T) string {
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
	insertBalance := func(accountID int64, date string, cents int64) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO balance_snapshots (account_id, date, current_cents)
			VALUES (?, ?, ?)
		`, accountID, date, cents); err != nil {
			t.Fatalf("insert balance: %v", err)
		}
	}

	checking := insertAccount("Everyday Checking", "checking", "acct-1")
	insertAccount("Rainy Day", "savings", "acct-2")
	investment := insertAccount("Investment Example", "investment", "acct-3")
	credit := insertAccount("Credit Example", "credit_card", "acct-4")
	insertBalance(checking, "2026-07-10", 100000)
	insertBalance(checking, "2026-07-20", 120000)
	insertBalance(investment, "2026-07-12", 450000)
	insertBalance(investment, "2026-07-21", 500000)
	insertBalance(credit, "2026-07-15", 300000)
	insertBalance(credit, "2026-07-22", 340000)
	return databasePath
}

func TestRunNetworthEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"networth"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"as_of: null",
		"assets: 0",
		"liabilities: 0",
		"networth: 0",
		"accounts: 0",
		"missing_balance: 0",
		"by_type[0]{type,count,balance}:",
		"run moneta link to connect an institution",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("networth empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunNetworthRendersLatestSnapshotSummary(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedNetworthCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"networth"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"as_of: 2026-07-22",
		"assets: 6200",
		"liabilities: 3400",
		"networth: 2800",
		"accounts: 4",
		"missing_balance: 1",
		"by_type[4]{type,count,balance}:",
		"checking,1,1200",
		"savings,1,null",
		"investment,1,5000",
		"credit_card,1,3400",
		"run moneta sync to pull balances",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("networth output missing %q:\n%s", want, out)
		}
	}
}

func TestRunNetworthAsOfCutoff(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedNetworthCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"networth", "--as-of", "2026-07-15"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"as_of: 2026-07-15",
		"assets: 5500",
		"liabilities: 3000",
		"networth: 2500",
		"checking,1,1000",
		"investment,1,4500",
		"credit_card,1,3000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("networth --as-of output missing %q:\n%s", want, out)
		}
	}
}

func TestRunNetworthJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedNetworthCommandDB(t))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"networth", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	want := `{"summary":{"as_of":"2026-07-22","assets":6200,"liabilities":3400,"networth":2800,"accounts":4,"missing_balance":1}`
	if !strings.HasPrefix(out, want) {
		t.Errorf("networth --json output = %q, want prefix %q", out, want)
	}
	if !strings.Contains(out, `{"type":"savings","count":1,"balance":null}`) {
		t.Errorf("networth --json missing nullable type balance: %q", out)
	}
}

func TestRunNetworthUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"networth", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"bad as-of", []string{"networth", "--as-of", "2026-02-30"}, filepath.Join(t.TempDir(), "db"), "--as-of must be a valid YYYY-MM-DD"},
		{"missing db", []string{"networth"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"networth", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
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

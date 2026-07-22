package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

// seedAccountsFixture writes one entity, two accounts (one with balance
// snapshots), and returns the database path. All data is fake.
func seedAccountsFixture(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close seed database: %v", err)
		}
	}()

	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}

	checking, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'checking', 'Everyday Checking', 'Fake Bank', 'plaid', 'acct-1')
	`, entityID)
	if err != nil {
		t.Fatalf("insert checking: %v", err)
	}
	checkingID, err := checking.LastInsertId()
	if err != nil {
		t.Fatalf("read checking id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, '2026-07-20', 99500)
	`, checkingID); err != nil {
		t.Fatalf("insert balance snapshot: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'credit_card', 'Travel Card', 'Fake Bank', 'plaid', 'acct-2')
	`, entityID); err != nil {
		t.Fatalf("insert card: %v", err)
	}
	return databasePath
}

func TestRunAccountsEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"accounts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "accounts: 0") {
		t.Errorf("accounts output missing empty summary:\n%s", out)
	}
	if !strings.Contains(out, "accounts[0]{name,type,balance,status}:") {
		t.Errorf("accounts output missing empty table header:\n%s", out)
	}
	if !strings.Contains(out, "moneta link") {
		t.Errorf("accounts empty-state hint should point at moneta link:\n%s", out)
	}
}

func TestRunAccountsRendersBalances(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedAccountsFixture(t))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"accounts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"accounts: 2",
		"active: 2",
		"checking: 1",
		"credit_card: 1",
		"Everyday Checking,checking,995,active",
		"Travel Card,credit_card,null,active",
		"hint: run moneta sync to pull balances",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("accounts output missing %q:\n%s", want, out)
		}
	}
}

func TestRunAccountsTypeFilterAndJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedAccountsFixture(t))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"accounts", "--type", "credit_card", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.Contains(out, `"accounts":1`) {
		t.Errorf("accounts --type output = %q, want one account", out)
	}
	if !strings.Contains(out, `"name":"Travel Card"`) {
		t.Errorf("accounts --type output missing Travel Card: %q", out)
	}
	if strings.Contains(out, "Everyday Checking") {
		t.Errorf("accounts --type credit_card should exclude checking: %q", out)
	}
}

// seedManyAccounts writes one entity and count fake checking accounts.
func seedManyAccounts(t *testing.T, count int) string {
	t.Helper()

	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close seed database: %v", err)
		}
	}()

	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	for i := 0; i < count; i++ {
		if _, err := db.Exec(`
			INSERT INTO accounts (
				entity_id, type, name, institution, provider, provider_account_id
			) VALUES (?, 'checking', ?, 'Fake Bank', 'plaid', ?)
		`, entityID, fmt.Sprintf("Account %02d", i), fmt.Sprintf("acct-%02d", i)); err != nil {
			t.Fatalf("insert account %d: %v", i, err)
		}
	}
	return databasePath
}

func TestRunAccountsTruncatesByDefault(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedManyAccounts(t, 25))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"accounts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "accounts[20]{") {
		t.Errorf("accounts output should show 20 rows by default:\n%s", out)
	}
	if !strings.Contains(out, "accounts: 25") {
		t.Errorf("summary should report the full account count under truncation:\n%s", out)
	}
	if !strings.Contains(out, "truncated: 20 of 25 shown (--full for all)") {
		t.Errorf("accounts output missing truncation line:\n%s", out)
	}

	stdout.Reset()
	code = run(context.Background(), []string{"accounts", "--full"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--full) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "accounts[25]{") {
		t.Errorf("accounts --full should show all 25 rows")
	}
	if strings.Contains(stdout.String(), "truncated:") {
		t.Errorf("accounts --full should not truncate:\n%s", stdout.String())
	}
}

func TestRunAccountsFilteredEmptyHintDoesNotPointAtLink(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedAccountsFixture(t))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"accounts", "--type", "investment"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "accounts: 0") {
		t.Errorf("filtered-empty output should show zero accounts:\n%s", out)
	}
	if !strings.Contains(out, "no accounts match --type investment") {
		t.Errorf("filtered-empty hint should name the filter:\n%s", out)
	}
	if strings.Contains(out, "moneta link") {
		t.Errorf("filtered-empty hint must not suggest linking:\n%s", out)
	}
}

func TestRunAccountsUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantCode int
		wantText string
	}{
		{
			name:     "positional argument",
			args:     []string{"accounts", "extra"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "does not accept positional arguments",
		},
		{
			name:     "unknown type",
			args:     []string{"accounts", "--type", "yacht"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: `unknown account type "yacht"`,
		},
		{
			name:     "invalid limit",
			args:     []string{"accounts", "--limit", "0"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "--limit must be at least 1",
		},
		{
			name:     "missing database path",
			args:     []string{"accounts"},
			dbPath:   "",
			wantCode: 2,
			wantText: "MONETA_DB_PATH or --db is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Errorf("run() code = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("run() stderr = %q, want %q", stderr.String(), test.wantText)
			}
		})
	}
}

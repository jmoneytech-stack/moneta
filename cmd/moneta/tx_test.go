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

// seedTxFixture writes one entity, one checking account, and count fake
// transactions (daily outflows of -100-i cents plus one inflow), returning
// the database path.
func seedTxFixture(t *testing.T, count int) string {
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
	account, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'checking', 'Everyday Checking', 'Fake Bank', 'plaid', 'acct-1')
	`, entityID)
	if err != nil {
		t.Fatalf("insert checking: %v", err)
	}
	accountID, err := account.LastInsertId()
	if err != nil {
		t.Fatalf("read checking id: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents,
			merchant_raw, merchant_norm, status, dedup_hash
		) VALUES (?, ?, '2026-07-01', 250000, 'EMPLOYER INC PAYROLL', 'Employer Inc', 'posted', 'hash-pay')
	`, accountID, entityID); err != nil {
		t.Fatalf("insert payroll: %v", err)
	}
	for i := 0; i < count; i++ {
		day := 2 + i%27
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents,
				merchant_raw, merchant_norm, status, dedup_hash
			) VALUES (?, ?, ?, ?, 'GROCERY MART #42', 'Grocery Mart', 'posted', ?)
		`, accountID, entityID,
			fmt.Sprintf("2026-07-%02d", day), int64(-100-i), fmt.Sprintf("hash-%d", i)); err != nil {
			t.Fatalf("insert transaction %d: %v", i, err)
		}
	}
	return databasePath
}

func TestRunTxEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"tx"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 0",
		"total: 0",
		"tx[0]{date,amount,merchant,status,account}:",
		"moneta link",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tx empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTxSummaryAndRows(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTxFixture(t, 2))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"tx"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 3",
		"total: 2497.99", // 250000 - 100 - 101 cents
		"inflow: 2500",
		"outflow: -2.01",
		"-1,Grocery Mart,posted,Everyday Checking",
		"2500,Employer Inc,posted,Everyday Checking",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tx output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTxFilters(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTxFixture(t, 5))

	tests := []struct {
		name      string
		args      []string
		wantCount string
	}{
		{"date range", []string{"tx", "--from", "2026-07-02", "--to", "2026-07-03"}, "count: 2"},
		{"search", []string{"tx", "--search", "employer"}, "count: 1"},
		{"account", []string{"tx", "--account", "everyday"}, "count: 6"},
		{"no match widening hint", []string{"tx", "--search", "zzz"}, "count: 0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.wantCount) {
				t.Errorf("tx output missing %q:\n%s", test.wantCount, stdout.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"tx", "--search", "zzz"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "widen the date range or relax") {
		t.Errorf("no-match hint should suggest widening:\n%s", stdout.String())
	}
}

func TestRunTxTruncatesByDefault(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTxFixture(t, 25))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"tx"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "tx[20]{") {
		t.Errorf("tx output should show 20 rows by default:\n%s", out)
	}
	if !strings.Contains(out, "count: 26") {
		t.Errorf("summary should count all matches under truncation:\n%s", out)
	}
	if !strings.Contains(out, "truncated: 20 of 26 shown") {
		t.Errorf("tx output missing truncation line:\n%s", out)
	}

	stdout.Reset()
	code = run(context.Background(), []string{"tx", "--full"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--full) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "tx[26]{") {
		t.Errorf("tx --full should show all 26 rows")
	}
}

func TestRunTxUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantCode int
		wantText string
	}{
		{
			name:     "positional argument",
			args:     []string{"tx", "extra"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "does not accept positional arguments",
		},
		{
			name:     "bad from date",
			args:     []string{"tx", "--from", "07/01/2026"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "--from must be a valid YYYY-MM-DD date",
		},
		{
			name:     "impossible date",
			args:     []string{"tx", "--to", "2026-02-30"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "--to must be a valid YYYY-MM-DD date",
		},
		{
			name:     "inverted range",
			args:     []string{"tx", "--from", "2026-07-20", "--to", "2026-07-01"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "--from must not be after --to",
		},
		{
			name:     "missing database path",
			args:     []string{"tx"},
			dbPath:   "",
			wantCode: 1,
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

func TestRunTxJSONFormat(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTxFixture(t, 1))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"tx", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, `{"summary":{"count":2`) {
		t.Errorf("tx --json output = %q, want compact JSON with summary first", out)
	}
	if !strings.Contains(out, `"amount":-1`) && !strings.Contains(out, `"amount":2500`) {
		t.Errorf("tx --json should emit amounts as numbers: %q", out)
	}
}

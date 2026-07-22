package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/store"
)

func seedSpendCommandDB(t *testing.T, merchantCount int) string {
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
	account, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'checking', 'Everyday Checking', 'Fake Bank', 'plaid', 'acct-1')
	`, entityID)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, err := account.LastInsertId()
	if err != nil {
		t.Fatalf("read account id: %v", err)
	}

	insert := func(
		date string,
		amount int64,
		merchant string,
		category any,
		status string,
		excluded int,
		hash string,
	) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, merchant_raw,
				merchant_norm, category_id, status, excluded, dedup_hash
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, accountID, entityID, date, amount, merchant, merchant,
			category, status, excluded, hash); err != nil {
			t.Fatalf("insert transaction %q: %v", hash, err)
		}
	}

	if merchantCount == 0 {
		insert("2026-07-10", -2000, "Grocery Mart", int64(7), "posted", 0, "food")
		insert("2026-07-11", -500, "Cafe Example", nil, "posted", 0, "cafe")
		insert("2026-07-12", -9000, "Transfer Example", int64(2), "posted", 1, "transfer")
		insert("2026-07-13", -3000, "Pending Shop", int64(7), "pending", 0, "pending")
		insert("2026-07-14", 100000, "Employer Example", int64(1), "posted", 0, "income")
		return databasePath
	}
	for i := 0; i < merchantCount; i++ {
		insert(
			"2026-07-10",
			int64(-100-i),
			fmt.Sprintf("Merchant %02d", i),
			nil,
			"posted",
			0,
			fmt.Sprintf("spend-%02d", i),
		)
	}
	return databasePath
}

func TestResolveSpendPeriod(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	tests := []struct {
		name        string
		periodValue string
		from        string
		to          string
		want        spendPeriod
		wantErr     bool
	}{
		{"default current local month", "", "", "", spendPeriod{"2026-07-01", "2026-07-31"}, false},
		{"explicit leap month", "2024-02", "", "", spendPeriod{"2024-02-01", "2024-02-29"}, false},
		{"custom inclusive dates", "", "2026-06-15", "2026-07-14", spendPeriod{"2026-06-15", "2026-07-14"}, false},
		{"invalid month", "2026-13", "", "", spendPeriod{}, true},
		{"month and dates conflict", "2026-07", "2026-07-01", "2026-07-31", spendPeriod{}, true},
		{"from only", "", "2026-07-01", "", spendPeriod{}, true},
		{"to only", "", "", "2026-07-31", spendPeriod{}, true},
		{"invalid custom date", "", "2026-02-30", "2026-03-01", spendPeriod{}, true},
		{"inverted custom dates", "", "2026-07-31", "2026-07-01", spendPeriod{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveSpendPeriod(test.periodValue, test.from, test.to, now)
			if (err != nil) != test.wantErr {
				t.Fatalf("resolveSpendPeriod() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("resolveSpendPeriod() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRunSpendEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"spend", "--period", "2026-07"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"from: 2026-07-01",
		"to: 2026-07-31",
		"count: 0",
		"total_spend: 0",
		"by_category[0]{category,spend,count}:",
		"by_merchant[0]{merchant,spend,count}:",
		"widen --period/--from/--to or run moneta sync",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("spend empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunSpendRendersPositiveSpendBreakdowns(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"spend", "--period", "2026-07"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 2",
		"total_spend: 25",
		"by_category[2]{category,spend,count}:",
		"Food and Drink,20,1",
		"Uncategorized,5,1",
		"by_merchant[2]{merchant,spend,count}:",
		"Grocery Mart,20,1",
		"Cafe Example,5,1",
		"run moneta tx --from 2026-07-01 --to 2026-07-31",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("spend output missing %q:\n%s", want, out)
		}
	}
	for _, excluded := range []string{"Transfer Example", "Pending Shop", "Employer Example"} {
		if strings.Contains(out, excluded) {
			t.Errorf("spend output should not include %q:\n%s", excluded, out)
		}
	}
}

func TestRunSpendJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"spend", "--period", "2026-07", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, `{"summary":{"from":"2026-07-01","to":"2026-07-31","count":2,"total_spend":25}`) {
		t.Errorf("spend --json output = %q", out)
	}
	if !strings.Contains(out, `"by_category":[{"category":"Food and Drink","spend":20,"count":1}`) {
		t.Errorf("spend --json missing category breakdown: %q", out)
	}
}

func TestRunSpendTruncatesEachBreakdown(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 25))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"spend", "--period", "2026-07"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "count: 25") || !strings.Contains(out, "by_merchant[20]{") {
		t.Errorf("spend output should preserve summary and show 20 merchants:\n%s", out)
	}
	if !strings.Contains(out, "merchant_truncated: 20 of 25 groups shown (--full for all)") {
		t.Errorf("spend output missing merchant truncation:\n%s", out)
	}
	if strings.Contains(out, "category_truncated:") {
		t.Errorf("single category should not be marked truncated:\n%s", out)
	}

	stdout.Reset()
	code = run(context.Background(),
		[]string{"spend", "--period", "2026-07", "--full"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--full) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "by_merchant[25]{") || strings.Contains(stdout.String(), "merchant_truncated:") {
		t.Errorf("spend --full should show every merchant:\n%s", stdout.String())
	}
}

func TestRunSpendUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"spend", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"bad month", []string{"spend", "--period", "2026-13"}, filepath.Join(t.TempDir(), "db"), "valid YYYY-MM"},
		{"mixed periods", []string{"spend", "--period", "2026-07", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "cannot be combined"},
		{"partial custom", []string{"spend", "--from", "2026-07-01"}, filepath.Join(t.TempDir(), "db"), "must be provided together"},
		{"invalid limit", []string{"spend", "--limit", "0"}, filepath.Join(t.TempDir(), "db"), "--limit must be at least 1"},
		{"missing db", []string{"spend", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"spend", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
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

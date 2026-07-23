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

func TestRunNetworthHistoryEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.Local)
	var stdout, stderr bytes.Buffer
	code := runNetworthAt(
		context.Background(),
		[]string{"--history", "3d"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runNetworthAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"from: 2026-07-20",
		"to: 2026-07-22",
		"days: 3",
		"history[3]{date,assets,liabilities,networth}:",
		"2026-07-20,0,0,0",
		"no balance snapshots on or before 2026-07-22",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty history output missing %q:\n%s", want, out)
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

func TestNetworthHistoryWindowBounds(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedNetworthCommandDB(t))
	now := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runNetworthAt(
		context.Background(),
		[]string{"--history", "5d"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runNetworthAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"from: 2026-07-18",
		"to: 2026-07-22",
		"days: 5",
		"history[5]{date,assets,liabilities,networth}:",
		"2026-07-18,5500,3000,2500",
		"2026-07-22,6200,3400,2800",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("networth --history output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runNetworthAt(
		context.Background(),
		[]string{"--history", "2d", "--json"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runNetworthAt(--json) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	jsonOutput := strings.TrimSpace(stdout.String())
	if !strings.Contains(jsonOutput, `"summary":{"from":"2026-07-21","to":"2026-07-22","days":2}`) ||
		!strings.Contains(jsonOutput, `"date":"2026-07-22","assets":6200,"liabilities":3400,"networth":2800`) {
		t.Errorf("networth history JSON = %q", jsonOutput)
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "bad history", args: []string{"--history", "week"}, want: "--history must use Nd form"},
		{name: "empty history", args: []string{"--history="}, want: "--history must use Nd form"},
		{name: "history and as-of", args: []string{"--history", "7d", "--as-of", "2026-07-22"}, want: "--history cannot be combined with --as-of"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runNetworthAt(context.Background(), test.args, &stdout, &stderr, now)
			if code != 2 {
				t.Errorf("runNetworthAt() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.want)
			}
		})
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

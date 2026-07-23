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

func seedTrendMoMCommandDB(t *testing.T) string {
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
	accountResult, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, 'checking', 'Everyday Checking', 'Fake Bank', 'plaid', 'trend-account')
	`, entityID)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, err := accountResult.LastInsertId()
	if err != nil {
		t.Fatalf("account id: %v", err)
	}
	insert := func(date string, amount int64, category any, status string, excluded int, hash string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, merchant_raw,
				merchant_norm, category_id, status, excluded, dedup_hash
			) VALUES (?, ?, ?, ?, 'Trend Merchant', 'Trend Merchant', ?, ?, ?, ?)
		`, accountID, entityID, date, amount, category, status, excluded, hash); err != nil {
			t.Fatalf("insert transaction %q: %v", hash, err)
		}
	}
	insert("2026-06-10", -3000, int64(7), "posted", 0, "prev-food")
	insert("2026-07-10", -4000, int64(7), "posted", 0, "this-food")
	insert("2026-06-11", -5000, int64(8), "posted", 0, "prev-general")
	insert("2026-07-11", -1000, int64(8), "posted", 0, "this-general")
	insert("2026-07-12", -2000, nil, "posted", 0, "this-unknown")
	insert("2026-07-13", -900000, int64(2), "posted", 1, "excluded")
	insert("2026-07-14", -800000, int64(7), "pending", 0, "pending")
	insert("2026-07-15", 700000, int64(1), "posted", 0, "income")
	return databasePath
}

func TestRunTrendsMoMRendersTOONAndJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTrendMoMCommandDB(t))
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runTrendsAt(
		context.Background(),
		[]string{"--metric", "mom", "--limit", "2"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"metric: mom",
		"this_from: 2026-07-01",
		"this_to: 2026-07-31",
		"prev_from: 2026-06-01",
		"prev_to: 2026-06-30",
		"spend_this: 70",
		"spend_prev: 80",
		"delta: -10",
		"categories: 3",
		"by_category[2]{category,spend_this,spend_prev,delta}:",
		"General Merchandise,10,50,-40",
		"Uncategorized,20,0,20",
		"truncated: 2 of 3 categories shown (--full for all)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trends output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runTrendsAt(
		context.Background(),
		[]string{"--metric", "mom", "--period", "2026-07", "--full", "--json"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	jsonOutput := strings.TrimSpace(stdout.String())
	if !strings.Contains(jsonOutput, `"summary":{"metric":"mom","this_from":"2026-07-01","this_to":"2026-07-31","prev_from":"2026-06-01","prev_to":"2026-06-30","spend_this":70,"spend_prev":80,"delta":-10,"categories":3}`) ||
		!strings.Contains(jsonOutput, `{"category":"Food and Drink","spend_this":40,"spend_prev":30,"delta":10}`) {
		t.Errorf("trends JSON = %q", jsonOutput)
	}
}

func seedTrendUtilizationCommandDB(t *testing.T) string {
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
	insertCard := func(name, providerID string) int64 {
		t.Helper()
		result, err := db.Exec(`
			INSERT INTO accounts (
				entity_id, type, name, institution, provider, provider_account_id
			) VALUES (?, 'credit_card', ?, 'Fake Bank', 'plaid', ?)
		`, entityID, name, providerID)
		if err != nil {
			t.Fatalf("insert card: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("card id: %v", err)
		}
		return id
	}
	cardID := insertCard("Credit Example", "util-card")
	missingLimitID := insertCard("Missing Limit Card", "util-missing")
	insertSnapshot := func(accountID int64, date string, balance int64, limit any) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO balance_snapshots (
				account_id, date, current_cents, limit_cents
			) VALUES (?, ?, ?, ?)
		`, accountID, date, balance, limit); err != nil {
			t.Fatalf("insert utilization snapshot: %v", err)
		}
	}
	insertSnapshot(cardID, "2026-07-01", 340000, int64(1000000))
	insertSnapshot(cardID, "2026-07-03", -5000, int64(1000000))
	insertSnapshot(missingLimitID, "2026-07-01", 25000, nil)
	return databasePath
}

func TestResolveTrendUtilizationPeriod(t *testing.T) {
	now := time.Date(2026, time.July, 4, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	tests := []struct {
		name            string
		history         string
		historyProvided bool
		period          string
		periodProvided  bool
		from            string
		fromProvided    bool
		to              string
		toProvided      bool
		want            readPeriod
		wantErr         bool
	}{
		{"default 30 days", "", false, "", false, "", false, "", false, readPeriod{"2026-06-05", "2026-07-04"}, false},
		{"explicit history", "4d", true, "", false, "", false, "", false, readPeriod{"2026-07-01", "2026-07-04"}, false},
		{"calendar month", "", false, "2026-07", true, "", false, "", false, readPeriod{"2026-07-01", "2026-07-31"}, false},
		{"custom dates", "", false, "", false, "2026-06-30", true, "2026-07-02", true, readPeriod{"2026-06-30", "2026-07-02"}, false},
		{"window conflict", "4d", true, "2026-07", true, "", false, "", false, readPeriod{}, true},
		{"empty explicit history", "", true, "", false, "", false, "", false, readPeriod{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveTrendUtilizationPeriod(
				test.history,
				test.historyProvided,
				test.period,
				test.periodProvided,
				test.from,
				test.fromProvided,
				test.to,
				test.toProvided,
				now,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("resolveTrendUtilizationPeriod() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Errorf("resolveTrendUtilizationPeriod() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRunTrendsUtilizationRendersTOONAndJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedTrendUtilizationCommandDB(t))
	now := time.Date(2026, time.July, 4, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runTrendsAt(
		context.Background(),
		[]string{"--metric", "utilization", "--history", "4d"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"metric: utilization",
		"from: 2026-07-01",
		"to: 2026-07-04",
		"days: 4",
		"accounts: 2",
		"missing_limit_days: 4",
		"history[4]{date,utilization,debt,limit,accounts}:",
		"2026-07-01,0.34,3400,10000,1",
		"2026-07-02,0.34,3400,10000,1",
		"2026-07-03,0,0,10000,1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("utilization output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runTrendsAt(
		context.Background(),
		[]string{
			"--metric", "utilization",
			"--from", "2026-07-01",
			"--to", "2026-07-02",
			"--json",
		},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	jsonOutput := strings.TrimSpace(stdout.String())
	if !strings.Contains(jsonOutput, `"summary":{"metric":"utilization","from":"2026-07-01","to":"2026-07-02","days":2,"accounts":2,"missing_limit_days":2}`) ||
		!strings.Contains(jsonOutput, `{"date":"2026-07-02","utilization":0.34,"debt":3400,"limit":10000,"accounts":1}`) {
		t.Errorf("utilization JSON = %q", jsonOutput)
	}
}

func TestRunTrendsUtilizationEmptyDefaultsToThirtyDays(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	now := time.Date(2026, time.July, 4, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))
	var stdout, stderr bytes.Buffer
	code := runTrendsAt(
		context.Background(),
		[]string{"--metric", "utilization"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"from: 2026-06-05",
		"to: 2026-07-04",
		"days: 30",
		"accounts: 0",
		"missing_limit_days: 0",
		"history[30]{date,utilization,debt,limit,accounts}:",
		"2026-06-05,null,0,0,0",
		"no credit-card accounts",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty utilization output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTrendsSavingsRendersTOONJSONAndMatchesCashflow(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runTrendsAt(
		context.Background(),
		[]string{"--metric", "savings"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"metric: savings",
		"from: 2026-07-01",
		"to: 2026-07-31",
		"count: 3",
		"inflow: 1000",
		"outflow: 25",
		"net: 975",
		"savings_rate: 0.975",
		"run moneta cashflow --from 2026-07-01 --to 2026-07-31",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("savings output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runTrendsAt(
		context.Background(),
		[]string{
			"--metric", "savings",
			"--from", "2026-07-01",
			"--to", "2026-07-31",
			"--json",
		},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	savingsJSON := strings.TrimSpace(stdout.String())
	wantSavings := `"summary":{"metric":"savings","from":"2026-07-01","to":"2026-07-31","count":3,"inflow":1000,"outflow":25,"net":975,"savings_rate":0.975}`
	if !strings.Contains(savingsJSON, wantSavings) {
		t.Errorf("savings JSON = %q, want %q", savingsJSON, wantSavings)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(
		context.Background(),
		[]string{"cashflow", "--period", "2026-07", "--json"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("run(cashflow JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	cashflowJSON := strings.TrimSpace(stdout.String())
	for _, field := range []string{
		`"count":3`,
		`"inflow":1000`,
		`"outflow":25`,
		`"net":975`,
		`"savings_rate":0.975`,
	} {
		if !strings.Contains(savingsJSON, field) || !strings.Contains(cashflowJSON, field) {
			t.Errorf("cashflow/savings parity missing %q:\nsavings=%s\ncashflow=%s",
				field, savingsJSON, cashflowJSON)
		}
	}
}

func TestRunTrendsSavingsZeroInflowUsesNullRate(t *testing.T) {
	// merchantCount=1 seeds one posted -$1 outflow and no inflow.
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 1))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"trends", "--metric", "savings", "--period", "2026-07",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 1",
		"inflow: 0",
		"outflow: 1",
		"net: -1",
		"savings_rate: null",
		"savings_rate is null because inflow is zero",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("zero-inflow savings output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTrendsMerchantsRendersTOONAndJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var stdout, stderr bytes.Buffer
	code := runTrendsAt(
		context.Background(),
		[]string{"--metric", "merchants", "--limit", "1"},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"metric: merchants",
		"from: 2026-07-01",
		"to: 2026-07-31",
		"spend: 25",
		"count: 2",
		"merchants: 2",
		"by_merchant[1]{merchant,spend,count}:",
		"Grocery Mart,20,1",
		"truncated: 1 of 2 merchants shown (--full for all)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("merchant trends output missing %q:\n%s", want, out)
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = runTrendsAt(
		context.Background(),
		[]string{
			"--metric", "merchants",
			"--from", "2026-07-01",
			"--to", "2026-07-31",
			"--full",
			"--json",
		},
		&stdout,
		&stderr,
		now,
	)
	if code != 0 {
		t.Fatalf("runTrendsAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	jsonOutput := strings.TrimSpace(stdout.String())
	if !strings.Contains(jsonOutput, `"summary":{"metric":"merchants","from":"2026-07-01","to":"2026-07-31","spend":25,"count":2,"merchants":2}`) ||
		!strings.Contains(jsonOutput, `{"merchant":"Cafe Example","spend":5,"count":1}`) {
		t.Errorf("merchant trends JSON = %q", jsonOutput)
	}
}

func TestRunTrendsMerchantsEmpty(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"trends", "--metric", "merchants", "--period", "2026-07",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"spend: 0",
		"count: 0",
		"merchants: 0",
		"by_merchant[0]{merchant,spend,count}:",
		"no posted spending in this period",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty merchant trends output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTrendsMoMEmpty(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"trends", "--metric", "mom", "--period", "2026-07",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"spend_this: 0",
		"spend_prev: 0",
		"delta: 0",
		"categories: 0",
		"by_category[0]{category,spend_this,spend_prev,delta}:",
		"no posted spending in either month",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty trends output missing %q:\n%s", want, out)
		}
	}
}

func TestRunTrendsUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"missing metric", []string{"trends", "--period", "2026-07"}, filepath.Join(t.TempDir(), "db"), "--metric is required"},
		{"unknown metric", []string{"trends", "--metric", "fixed-variable"}, filepath.Join(t.TempDir(), "db"), "unknown --metric"},
		{"mom custom dates rejected", []string{"trends", "--metric", "mom", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "--metric mom requires --period"},
		{"mom invalid period", []string{"trends", "--metric", "mom", "--period", "2026-13"}, filepath.Join(t.TempDir(), "db"), "valid YYYY-MM"},
		{"merchants month and dates conflict", []string{"trends", "--metric", "merchants", "--period", "2026-07", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "cannot be combined"},
		{"merchants requires both dates", []string{"trends", "--metric", "merchants", "--from", "2026-07-01"}, filepath.Join(t.TempDir(), "db"), "must be provided together"},
		{"merchants invalid custom date", []string{"trends", "--metric", "merchants", "--from", "2026-02-30", "--to", "2026-03-01"}, filepath.Join(t.TempDir(), "db"), "valid YYYY-MM-DD"},
		{"mom rejects history", []string{"trends", "--metric", "mom", "--history", "30d"}, filepath.Join(t.TempDir(), "db"), "--history is supported only"},
		{"merchants reject history", []string{"trends", "--metric", "merchants", "--history", "30d"}, filepath.Join(t.TempDir(), "db"), "--history is supported only"},
		{"utilization window conflict", []string{"trends", "--metric", "utilization", "--history", "30d", "--period", "2026-07"}, filepath.Join(t.TempDir(), "db"), "cannot be combined"},
		{"utilization invalid history", []string{"trends", "--metric", "utilization", "--history", "0d"}, filepath.Join(t.TempDir(), "db"), "at least 1 day"},
		{"utilization rejects row limit", []string{"trends", "--metric", "utilization", "--limit", "5"}, filepath.Join(t.TempDir(), "db"), "--limit/--full are unsupported"},
		{"savings rejects history", []string{"trends", "--metric", "savings", "--history", "30d"}, filepath.Join(t.TempDir(), "db"), "--history is supported only"},
		{"savings rejects row limit", []string{"trends", "--metric", "savings", "--full"}, filepath.Join(t.TempDir(), "db"), "--limit/--full are unsupported"},
		{"savings month and dates conflict", []string{"trends", "--metric", "savings", "--period", "2026-07", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "cannot be combined"},
		{"mom missing database", []string{"trends", "--metric", "mom", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
		{"merchants missing database", []string{"trends", "--metric", "merchants", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
		{"utilization missing database", []string{"trends", "--metric", "utilization", "--history", "1d"}, "", "MONETA_DB_PATH or --db is required"},
		{"savings missing database", []string{"trends", "--metric", "savings", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
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

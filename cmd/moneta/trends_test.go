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
		{"unknown metric", []string{"trends", "--metric", "merchants"}, filepath.Join(t.TempDir(), "db"), "unknown --metric"},
		{"custom dates rejected", []string{"trends", "--metric", "mom", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "--metric mom requires --period"},
		{"invalid period", []string{"trends", "--metric", "mom", "--period", "2026-13"}, filepath.Join(t.TempDir(), "db"), "valid YYYY-MM"},
		{"missing database", []string{"trends", "--metric", "mom", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
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

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

func TestRunAnomaliesTOONJSON(t *testing.T) {
	databasePath := seedAnomaliesCommandDB(t)
	t.Setenv(databasePathEnvironment, databasePath)
	now := time.Date(2026, time.May, 20, 23, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var toonOut, toonErr bytes.Buffer
	if code := runAnomaliesAt(context.Background(), nil, &toonOut, &toonErr, now); code != 0 {
		t.Fatalf("runAnomaliesAt(TOON) code = %d, want 0 (stderr %q)", code, toonErr.String())
	}
	for _, want := range []string{
		"period: 2026-04", "count: 1", "skipped_overflow: 0",
		"anomalies[1]{category,spend,baseline,deviation_ratio}:",
		"Command Spike,310,100,2.1",
	} {
		if !strings.Contains(toonOut.String(), want) {
			t.Errorf("TOON output missing %q:\n%s", want, toonOut.String())
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	if code := runAnomaliesAt(
		context.Background(), []string{"--period", "2026-04", "--json"},
		&jsonOut, &jsonErr, now,
	); code != 0 {
		t.Fatalf("runAnomaliesAt(JSON) code = %d, want 0 (stderr %q)", code, jsonErr.String())
	}
	for _, want := range []string{
		`"summary":{"period":"2026-04","count":1,"skipped_overflow":0}`,
		`"anomalies":[{"category":"Command Spike","spend":310,"baseline":100,"deviation_ratio":2.1}]`,
	} {
		if !strings.Contains(jsonOut.String(), want) {
			t.Errorf("JSON output missing %q:\n%s", want, jsonOut.String())
		}
	}

	for _, test := range []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"future period", []string{"--period", "2026-06"}, databasePath, "must not be in the future"},
		{"invalid period", []string{"--period", "2026-13"}, databasePath, "valid YYYY-MM"},
		{"unknown flag", []string{"--bogus"}, databasePath, "flag provided but not defined"},
		{"positional", []string{"extra"}, databasePath, "does not accept positional"},
		{"missing db", nil, "", "MONETA_DB_PATH or --db is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			var stdout, stderr bytes.Buffer
			code := runAnomaliesAt(context.Background(), test.args, &stdout, &stderr, now)
			if code != 2 {
				t.Errorf("runAnomaliesAt() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantText)
			}
		})
	}
}

func seedAnomaliesCommandDB(t *testing.T) string {
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
			entity_id, type, name, provider, provider_account_id
		) VALUES (?, 'checking', 'Command Checking', 'plaid', 'command-anomalies')
	`, entityID)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, err := accountResult.LastInsertId()
	if err != nil {
		t.Fatalf("account id: %v", err)
	}
	categoryResult, err := db.Exec(`
		INSERT INTO categories (name, kind) VALUES ('Command Spike', 'expense')
	`)
	if err != nil {
		t.Fatalf("insert category: %v", err)
	}
	categoryID, err := categoryResult.LastInsertId()
	if err != nil {
		t.Fatalf("category id: %v", err)
	}
	for index, fixture := range []struct {
		date   string
		amount int64
	}{
		{"2026-01-15", -10000},
		{"2026-02-15", -10000},
		{"2026-03-15", -10000},
		{"2026-04-15", -31000},
	} {
		if _, err := db.Exec(`
			INSERT INTO transactions (
				account_id, entity_id, date, amount_cents, category_id,
				status, excluded, is_transfer, dedup_hash
			) VALUES (?, ?, ?, ?, ?, 'posted', 0, 0, ?)
		`, accountID, entityID, fixture.date, fixture.amount, categoryID,
			"command-anomaly-"+string(rune('a'+index))); err != nil {
			t.Fatalf("insert transaction: %v", err)
		}
	}
	return databasePath
}

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

func TestRunBillsTOONJSONAndUsage(t *testing.T) {
	databasePath := seedBillsCommandDB(t)
	t.Setenv(databasePathEnvironment, databasePath)
	now := time.Date(2026, time.July, 1, 23, 0, 0, 0, time.FixedZone("local", -7*60*60))

	var toonOut, toonErr bytes.Buffer
	if code := runBillsAt(context.Background(), nil, &toonOut, &toonErr, now); code != 0 {
		t.Fatalf("runBillsAt(TOON) code = %d, want 0 (stderr %q)", code, toonErr.String())
	}
	for _, want := range []string{
		"as_of: 2026-07-01", "through: 2026-07-31", "days: 30", "count: 2",
		"status: ok", "bills[2]{date,name,amount,source,kind,date_source,due_status}:",
		"2026-07-10,Streambox Example,18,recurring,subscription,detected_schedule,upcoming",
		"2026-07-20,Travel Card,32,card_due,bill,provider_reported,upcoming",
	} {
		if !strings.Contains(toonOut.String(), want) {
			t.Errorf("TOON output missing %q:\n%s", want, toonOut.String())
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	if code := runBillsAt(context.Background(), []string{"--days", "10", "--json"}, &jsonOut, &jsonErr, now); code != 0 {
		t.Fatalf("runBillsAt(JSON) code = %d, want 0 (stderr %q)", code, jsonErr.String())
	}
	for _, want := range []string{
		`"summary":{"as_of":"2026-07-01","through":"2026-07-11","days":10,"count":1`,
		`"detector":{"status":"ok","last_run_at":"2026-07-01T12:00:00.000Z","last_success_at":"2026-07-01T12:00:00.000Z","last_skipped_overflow":0}`,
		`"bills":[{"date":"2026-07-10","name":"Streambox Example","amount":18,"source":"recurring","kind":"subscription","date_source":"detected_schedule","due_status":"upcoming"}]`,
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
		{"zero days", []string{"--days", "0"}, databasePath, "--days must be from 1 to 366"},
		{"too many days", []string{"--days", "367"}, databasePath, "--days must be from 1 to 366"},
		{"unknown flag", []string{"--bogus"}, databasePath, "flag provided but not defined"},
		{"positional", []string{"extra"}, databasePath, "does not accept positional"},
		{"missing db", nil, "", "MONETA_DB_PATH or --db is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			var stdout, stderr bytes.Buffer
			code := runBillsAt(context.Background(), test.args, &stdout, &stderr, now)
			if code != 2 {
				t.Errorf("runBillsAt() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantText)
			}
		})
	}
}

func seedBillsCommandDB(t *testing.T) string {
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
	if err := store.UpsertDetectorState(ctx, db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-07-01T12:00:00.000Z",
		LastSuccessAt: "2026-07-01T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("seed detector state: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, schedule_anchor_day
		) VALUES (?, 'Streambox Example', 'subscription', 'monthly', -1800,
			'2026-07-10', 'detected', 1, 'streambox example', -1, 10)
	`, entityID); err != nil {
		t.Fatalf("insert recurring bill: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, provider, provider_account_id
		) VALUES (?, 'credit_card', 'Travel Card', 'plaid', 'bills-travel-card')
	`, entityID)
	if err != nil {
		t.Fatalf("insert card: %v", err)
	}
	cardID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("card id: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credit_terms (
			account_id, min_payment_cents, due_day, next_payment_due_date
		) VALUES (?, 3200, 20, '2026-07-20')
	`, cardID); err != nil {
		t.Fatalf("insert card terms: %v", err)
	}
	return databasePath
}

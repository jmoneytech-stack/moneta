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

func TestRunRecurringTOONJSON(t *testing.T) {
	databasePath := seedRecurringCommandDB(t)
	t.Setenv(databasePathEnvironment, databasePath)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

	t.Run("TOON", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runRecurringAt(context.Background(), nil, &stdout, &stderr, now)
		if code != 0 {
			t.Fatalf("runRecurringAt() code = %d, want 0 (stderr %q)", code, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"count: 2",
			"detector:",
			"status: ok",
			"subscription: 1",
			"bill: 0",
			"income: 0",
			"monthly_equivalent: -100",
			"monthly_equivalent_unconverted: 0",
			"recurring[2]{name,kind,cadence,expected,next_expected_date,drift_pct,drift,active,source}:",
			"Streambox Example,subscription,monthly,-100,2026-09-01,20,true,true,detected",
			"Manual Bill Example,bill,monthly,-50,2026-10-01,null,false,true,manual",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("recurring TOON missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("JSON and kind filter", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runRecurringAt(
			context.Background(), []string{"--json", "--kind", "subscription"},
			&stdout, &stderr, now,
		)
		if code != 0 {
			t.Fatalf("runRecurringAt(JSON) code = %d, want 0 (stderr %q)", code, stderr.String())
		}
		out := strings.TrimSpace(stdout.String())
		for _, want := range []string{
			`"count":1`,
			`"detector":{"status":"ok"`,
			`"by_kind":{"subscription":1,"bill":0,"income":0}`,
			`"monthly_equivalent":-100`,
			`{"name":"Streambox Example","kind":"subscription","cadence":"monthly","expected":-100,"next_expected_date":"2026-09-01","drift_pct":20,"drift":true,"active":true,"source":"detected"}`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("recurring JSON missing %q: %s", want, out)
			}
		}
		if strings.Contains(out, "Manual Bill Example") {
			t.Errorf("kind filter leaked manual bill row: %s", out)
		}
	})

	for _, test := range []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"invalid kind", []string{"--kind", "other"}, databasePath, "--kind must be"},
		{"unknown flag", []string{"--bogus"}, databasePath, "flag provided but not defined"},
		{"positional", []string{"extra"}, databasePath, "does not accept positional"},
		{"missing database", nil, "", "MONETA_DB_PATH or --db is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			var stdout, stderr bytes.Buffer
			code := runRecurringAt(context.Background(), test.args, &stdout, &stderr, now)
			if code != 2 || !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("runRecurringAt(%v) = code %d / stderr %q, want 2 / %q",
					test.args, code, stderr.String(), test.wantText)
			}
		})
	}
}

func seedRecurringCommandDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open recurring command database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close recurring command database: %v", err)
		}
	}()
	entityID, err := store.EnsureDefaultEntity(ctx, db)
	if err != nil {
		t.Fatalf("EnsureDefaultEntity() error: %v", err)
	}
	if err := store.UpsertDetectorState(ctx, db, store.DetectorState{
		Status: "ok", LastRunAt: "2026-08-15T12:00:00.000Z",
		LastSuccessAt: "2026-08-15T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("seed recurring detector state: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_items (
			entity_id, name, kind, cadence, expected_cents, next_expected_date,
			source, is_active, detect_key, amount_sign, last_matched_cents,
			schedule_anchor_day
		) VALUES
			(?, 'Streambox Example', 'subscription', 'monthly', -10000,
			 '2026-09-01', 'detected', 1, 'streambox example', -1, -12000, 1),
			(?, 'Manual Bill Example', 'bill', 'monthly', -5000,
			 '2026-10-01', 'manual', 1, '', 0, NULL, NULL)
	`, entityID, entityID); err != nil {
		t.Fatalf("seed recurring command rows: %v", err)
	}
	return databasePath
}

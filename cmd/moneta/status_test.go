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

func TestRunStatusEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "items: 0") {
		t.Errorf("status output missing empty summary:\n%s", out)
	}
	if !strings.Contains(out, "items[0]{provider,item,institution,status,accounts,transactions,last_sync}:") {
		t.Errorf("status output missing empty items header:\n%s", out)
	}
	if !strings.Contains(out, "hint: \"run moneta link") {
		t.Errorf("status output missing link hint:\n%s", out)
	}
}

func TestRunStatusReportsItemsAndHealth(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	t.Setenv(databasePathEnvironment, databasePath)
	seedStatusItems(t, databasePath, []seedItem{
		{itemID: "item-one", institution: "First Bank"},
		{itemID: "item-two", institution: "Second Union", status: "login_required"},
	})

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("run() code = %d, want 3 reconnection-needed (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"items: 2",
		"needs_attention: 1",
		"plaid,item-one,First Bank,ok,0,0,\"\"",
		"plaid,item-two,Second Union,login_required,0,0,\"\"",
		"hint: re-run moneta link to reconnect",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestRunStatusJSONFormat(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	t.Setenv(databasePathEnvironment, databasePath)
	seedStatusItems(t, databasePath, []seedItem{
		{itemID: "item-one", institution: "First Bank", lastSyncedAt: "2026-07-20T14:03:11.123Z"},
	})

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(out, `{"summary":{"items":1`) {
		t.Errorf("status --json output = %q, want compact JSON with summary first", out)
	}
	if !strings.Contains(out, `"last_sync":"2026-07-20T14:03:11.123Z"`) {
		t.Errorf("status --json output missing last_sync: %q", out)
	}
	if !strings.HasSuffix(out, "}") || strings.Contains(out, "\n") {
		t.Errorf("status --json should be one compact line: %q", out)
	}
}

func TestRunStatusTruncatesByDefault(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	t.Setenv(databasePathEnvironment, databasePath)
	seeds := make([]seedItem, 25)
	for i := range seeds {
		seeds[i] = seedItem{itemID: fmt.Sprintf("item-%02d", i), institution: "Bank"}
	}
	seedStatusItems(t, databasePath, seeds)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "items[20]{") {
		t.Errorf("status output should show 20 rows by default:\n%s", out)
	}
	if !strings.Contains(out, "truncated: 20 of 25 shown (--full for all)") {
		t.Errorf("status output missing truncation line:\n%s", out)
	}
	if !strings.Contains(out, "items: 25") {
		t.Errorf("summary should report the full item count under truncation:\n%s", out)
	}

	stdout.Reset()
	code = run(context.Background(), []string{"status", "--full"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--full) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "items[25]{") {
		t.Errorf("status --full should show all 25 rows")
	}
	if strings.Contains(stdout.String(), "truncated:") {
		t.Errorf("status --full should not truncate:\n%s", stdout.String())
	}
}

func TestRunStatusTruncationPrefersAttentionRows(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "moneta.db")
	t.Setenv(databasePathEnvironment, databasePath)
	seeds := make([]seedItem, 25)
	for i := range seeds {
		seeds[i] = seedItem{itemID: fmt.Sprintf("item-%02d", i), institution: "Bank"}
	}
	// The attention row sorts last by item id, so a naive window would hide it.
	seeds[24].status = "login_required"
	seedStatusItems(t, databasePath, seeds)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("run() code = %d, want 3 reconnection-needed (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "plaid,item-24,Bank,login_required") {
		t.Errorf("truncated window should include the attention row:\n%s", out)
	}
	if !strings.Contains(out, "items[20]{") {
		t.Errorf("window should still hold 20 rows:\n%s", out)
	}
}

func TestRunStatusUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantCode int
		wantText string
	}{
		{
			name:     "positional argument",
			args:     []string{"status", "extra"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "does not accept positional arguments",
		},
		{
			name:     "invalid limit",
			args:     []string{"status", "--limit", "0"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "--limit must be at least 1",
		},
		{
			name:     "unknown flag",
			args:     []string{"status", "--nope"},
			dbPath:   filepath.Join(t.TempDir(), "moneta.db"),
			wantCode: 2,
			wantText: "flag provided but not defined",
		},
		{
			name:     "missing database path",
			args:     []string{"status"},
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

type seedItem struct {
	itemID       string
	institution  string
	status       string
	lastSyncedAt string
}

// seedStatusItems writes provider items directly, with placeholder
// ciphertext; status never decrypts credentials.
func seedStatusItems(t *testing.T, databasePath string, items []seedItem) {
	t.Helper()

	ctx := context.Background()
	db, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close seed database: %v", err)
		}
	}()

	for _, item := range items {
		if _, err := store.SaveProviderItem(ctx, db, store.ProviderItemSecret{
			Provider:              plaidProviderName,
			ItemID:                item.itemID,
			Institution:           item.institution,
			AccessTokenCiphertext: []byte("placeholder-ciphertext"),
		}); err != nil {
			t.Fatalf("save provider item %q: %v", item.itemID, err)
		}
		if item.status != "" {
			if _, err := db.ExecContext(ctx,
				"UPDATE provider_items SET status = ? WHERE item_id = ?",
				item.status, item.itemID,
			); err != nil {
				t.Fatalf("set status for %q: %v", item.itemID, err)
			}
		}
		if item.lastSyncedAt != "" {
			if _, err := db.ExecContext(ctx,
				"UPDATE provider_items SET last_synced_at = ? WHERE item_id = ?",
				item.lastSyncedAt, item.itemID,
			); err != nil {
				t.Fatalf("set last_synced_at for %q: %v", item.itemID, err)
			}
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUsageAndUnknownCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantText string
	}{
		{name: "help", args: []string{"help"}, wantCode: 0, wantText: "usage: moneta"},
		{name: "missing", wantCode: 2, wantText: "usage: moneta"},
		{name: "unknown", args: []string{"unknown"}, wantCode: 2, wantText: "unknown command"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Errorf("run() code = %d, want %d", code, test.wantCode)
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.wantText) {
				t.Errorf("run() output = %q, want %q", stdout.String()+stderr.String(), test.wantText)
			}
		})
	}
}

func TestRunLinkRequiresDatabasePathBeforeCredentials(t *testing.T) {
	t.Setenv(databasePathEnvironment, "")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"link"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "MONETA_DB_PATH or --db is required") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

func TestRunLinkRejectsBroadListenAddress(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	t.Setenv("PLAID_CLIENT_ID", "client-fake")
	t.Setenv("PLAID_SECRET", "secret-fake")
	t.Setenv(
		"MONETA_ENCRYPTION_KEY",
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
	)
	var stdout, stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"link", "--listen", "0.0.0.0:0"},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "must listen on 127.0.0.1") {
		t.Errorf("run() error = %q", stderr.String())
	}
}

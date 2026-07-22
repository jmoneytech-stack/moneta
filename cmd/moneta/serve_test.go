package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunServeUsageAndConfigurationErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		apiKey   string
		wantText string
	}{
		{"positional", []string{"serve", "extra"}, filepath.Join(t.TempDir(), "db"), "fake-key", "does not accept positional"},
		{"unknown flag", []string{"serve", "--bogus"}, filepath.Join(t.TempDir(), "db"), "fake-key", "flag provided but not defined"},
		{"missing database", []string{"serve"}, "", "fake-key", "MONETA_DB_PATH or --db is required"},
		{"missing API key", []string{"serve"}, filepath.Join(t.TempDir(), "db"), "", "MONETA_API_KEY or --api-key is required"},
		{"malformed listen", []string{"serve", "--listen", "127.0.0.1"}, filepath.Join(t.TempDir(), "db"), "fake-key", "host:port"},
		{"non-loopback refused", []string{"serve", "--listen", "0.0.0.0:8080"}, filepath.Join(t.TempDir(), "db"), "fake-key", "requires --allow-non-loopback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(databasePathEnvironment, test.dbPath)
			t.Setenv(apiKeyEnvironment, test.apiKey)
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), test.args, &stdout, &stderr)
			if code != 2 {
				t.Errorf("run() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.wantText) {
				t.Errorf("stderr = %q, want %q", stderr.String(), test.wantText)
			}
			if test.apiKey != "" && strings.Contains(stderr.String(), test.apiKey) {
				t.Error("serve error output leaked API key")
			}
		})
	}
}

func TestRunServeWarnsWhenAPIKeyComesFromFlag(t *testing.T) {
	flagKey := "fake-flag-key"
	environmentKey := "fake-environment-key"
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	t.Setenv(apiKeyEnvironment, environmentKey)

	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"serve",
		"--api-key", flagKey,
		"--listen", "127.0.0.1",
	}, io.Discard, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), apiKeyFlagWarning) {
		t.Errorf("stderr = %q, want warning %q", stderr.String(), apiKeyFlagWarning)
	}
	if strings.Contains(stderr.String(), flagKey) || strings.Contains(stderr.String(), environmentKey) {
		t.Error("serve warning output leaked an API key")
	}
}

func TestRunServeStopsCleanlyOnCanceledContext(t *testing.T) {
	apiKey := "fake-clean-stop-key"
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	t.Setenv(apiKeyEnvironment, apiKey)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stderr synchronizedBuffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- run(ctx, []string{"serve", "--listen", "127.0.0.1:0"}, io.Discard, &stderr)
	}()

	startupTimeout := time.NewTimer(10 * time.Second)
	defer startupTimeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(stderr.String(), "REST listening on 127.0.0.1:") {
		select {
		case code := <-codeCh:
			t.Fatalf("run() exited before startup with code %d (stderr %q)", code, stderr.String())
		case <-startupTimeout.C:
			cancel()
			<-codeCh
			t.Fatalf("run() did not start in time (stderr %q)", stderr.String())
		case <-ticker.C:
		}
	}

	cancel()
	code := <-codeCh
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "authentication enabled") {
		t.Errorf("startup log = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), apiKeyFlagWarning) {
		t.Errorf("environment API key should not produce flag warning: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), apiKey) {
		t.Error("startup log leaked API key")
	}
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

package main

import (
	"bytes"
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/toon"
)

func TestSavingsRateNumber(t *testing.T) {
	tests := []struct {
		name    string
		net     int64
		inflow  int64
		want    string
		wantNil bool
	}{
		{"twenty eight percent", 700, 2500, "0.28", false},
		{"truncate recurring fraction", 1, 3, "0.3333", false},
		{"negative rate", -1, 3, "-0.3333", false},
		{"zero net", 0, 2500, "0", false},
		{"one hundred percent", 2500, 2500, "1", false},
		{"integer boundary", math.MinInt64, math.MaxInt64, "-1", false},
		{"zero inflow", -100, 0, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := savingsRateNumber(test.net, test.inflow)
			if test.wantNil {
				if got != nil {
					t.Errorf("savingsRateNumber() = %q, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("savingsRateNumber() = nil")
			}
			if string(*got) != test.want {
				t.Errorf("savingsRateNumber() = %q, want %q", *got, test.want)
			}
			if !toon.ValidNumber(*got) {
				t.Errorf("savingsRateNumber() = %q is not canonical", *got)
			}
		})
	}
}

func TestRunCashflowEmptyDatabase(t *testing.T) {
	t.Setenv(databasePathEnvironment, filepath.Join(t.TempDir(), "moneta.db"))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cashflow", "--period", "2026-07"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"from: 2026-07-01",
		"to: 2026-07-31",
		"count: 0",
		"inflow: 0",
		"outflow: 0",
		"net: 0",
		"savings_rate: null",
		"widen --period/--from/--to or run moneta sync",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cashflow empty output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCashflowRendersSummary(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cashflow", "--period", "2026-07"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"count: 3",
		"inflow: 1000",
		"outflow: 25",
		"net: 975",
		"savings_rate: 0.975",
		"run moneta spend --from 2026-07-01 --to 2026-07-31",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cashflow output missing %q:\n%s", want, out)
		}
	}
	for _, excluded := range []string{"Transfer Example", "Pending Shop"} {
		if strings.Contains(out, excluded) {
			t.Errorf("cashflow output should not include %q:\n%s", excluded, out)
		}
	}
}

func TestRunCashflowZeroInflowUsesNullRate(t *testing.T) {
	// merchantCount=1 seeds one posted -$1 outflow and no inflow.
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 1))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"cashflow", "--period", "2026-07"}, &stdout, &stderr)
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
			t.Errorf("cashflow output missing %q:\n%s", want, out)
		}
	}
}

func TestRunCashflowJSON(t *testing.T) {
	t.Setenv(databasePathEnvironment, seedSpendCommandDB(t, 0))
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"cashflow", "--period", "2026-07", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := strings.TrimSpace(stdout.String())
	want := `{"summary":{"from":"2026-07-01","to":"2026-07-31","count":3,"inflow":1000,"outflow":25,"net":975,"savings_rate":0.975}`
	if !strings.HasPrefix(out, want) {
		t.Errorf("cashflow --json output = %q, want prefix %q", out, want)
	}
}

func TestRunCashflowUsageAndConfigErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dbPath   string
		wantText string
	}{
		{"positional", []string{"cashflow", "extra"}, filepath.Join(t.TempDir(), "db"), "does not accept positional"},
		{"bad month", []string{"cashflow", "--period", "2026-13"}, filepath.Join(t.TempDir(), "db"), "valid YYYY-MM"},
		{"mixed periods", []string{"cashflow", "--period", "2026-07", "--from", "2026-07-01", "--to", "2026-07-31"}, filepath.Join(t.TempDir(), "db"), "cannot be combined"},
		{"partial custom", []string{"cashflow", "--from", "2026-07-01"}, filepath.Join(t.TempDir(), "db"), "must be provided together"},
		{"missing db", []string{"cashflow", "--period", "2026-07"}, "", "MONETA_DB_PATH or --db is required"},
		{"unknown flag", []string{"cashflow", "--bogus"}, filepath.Join(t.TempDir(), "db"), "flag provided but not defined"},
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

package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func renderJSON(t *testing.T, dashboard store.DashboardReport) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := cli.Render(&buffer, Dashboard(dashboard), cli.FormatJSON); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	return strings.TrimSpace(buffer.String())
}

func populatedDashboard() store.DashboardReport {
	return store.DashboardReport{
		AsOf: "2026-07-22",
		From: "2026-07-01",
		To:   "2026-07-31",
		Networth: store.NetworthReport{
			AsOf:             "2026-07-22",
			AssetsCents:      1070000,
			LiabilitiesCents: 855000,
			NetworthCents:    215000,
			Accounts:         6,
		},
		CashCents:             170000,
		CashAccounts:          2,
		CardCount:             2,
		CardDebtCents:         355000,
		UtilizationCards:      1,
		UtilizationDebtCents:  340000,
		UtilizationLimitCents: 1000000,
		Spend:                 store.SpendSummary{Count: 1, SpendCents: 2500},
		Cashflow: store.CashflowSummary{
			Count: 2, InflowCents: 100000, OutflowCents: 2500, NetCents: 97500,
		},
		Sync: store.DashboardSync{Items: 1},
	}
}

func TestDashboardDocumentShape(t *testing.T) {
	out := renderJSON(t, populatedDashboard())
	for _, want := range []string{
		`"summary":{"as_of":"2026-07-22"}`,
		`"networth":{"assets":10700,"liabilities":8550,"networth":2150,"accounts":6,"missing_balance":0}`,
		`"cash":{"balance":1700,"accounts":2,"note":"checking + savings latest balances"}`,
		`"credit":{"utilization":0.34,"total_debt":3550,"cards":2}`,
		`"spend_month":{"from":"2026-07-01","to":"2026-07-31","total":25,"count":1}`,
		`"cashflow_month":{"inflow":1000,"outflow":25,"net":975,"savings_rate":0.975,"count":2}`,
		`"sync":{"items":1,"needs_attention":0,"login_required":0}`,
		`"upcoming_bills":null`,
		`"anomalies":null`,
		`"phase4_note":"` + Phase4Note + `"`,
		`"hint":"run moneta spend or moneta trends for the breakdown behind these totals"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard document missing %q:\n%s", want, out)
		}
	}
}

// The Phase 4 slots must never render as a value an agent could read as real
// data. Zero and an empty list are both indistinguishable from "none found".
func TestDashboardPhase4SlotsAreAlwaysNull(t *testing.T) {
	for name, dashboard := range map[string]store.DashboardReport{
		"populated": populatedDashboard(),
		"empty":     {From: "2026-07-01", To: "2026-07-31"},
	} {
		t.Run(name, func(t *testing.T) {
			out := renderJSON(t, dashboard)
			for _, want := range []string{`"upcoming_bills":null`, `"anomalies":null`} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, unwanted := range []string{
				`"upcoming_bills":0`, `"anomalies":0`,
				`"upcoming_bills":[]`, `"anomalies":[]`,
				`"upcoming_bills":""`, `"anomalies":""`,
			} {
				if strings.Contains(out, unwanted) {
					t.Errorf("fabricated Phase 4 value %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

func TestDashboardNullsUndefinedValues(t *testing.T) {
	out := renderJSON(t, store.DashboardReport{From: "2026-07-01", To: "2026-07-31"})
	for _, want := range []string{
		// No snapshot yet, so no fabricated "today".
		`"as_of":null`,
		// No card with a usable limit, so utilization is undefined, not 0%.
		`"utilization":null`,
		// No inflow, so the savings rate has no denominator.
		`"savings_rate":null`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty dashboard missing %q:\n%s", want, out)
		}
	}
}

// A zero or negative portfolio limit leaves utilization undefined even when
// UtilizationCards is non-zero, so a bad limit never reads as 0%.
func TestDashboardUtilizationNullOnUnusableLimit(t *testing.T) {
	dashboard := populatedDashboard()
	dashboard.UtilizationLimitCents = 0
	if out := renderJSON(t, dashboard); !strings.Contains(out, `"utilization":null`) {
		t.Errorf("zero portfolio limit did not null utilization:\n%s", out)
	}
}

func TestDashboardHintPrioritizesReconnection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*store.DashboardReport)
		want   string
	}{
		{
			"login required wins over every other signal",
			func(d *store.DashboardReport) {
				d.Sync = store.DashboardSync{Items: 2, NeedsAttention: 1, LoginRequired: 1}
				d.Networth.MissingBalance = 3
			},
			"re-run moneta link to reconnect items with status login_required",
		},
		{
			"no items yet",
			func(d *store.DashboardReport) { d.Sync = store.DashboardSync{} },
			"run moneta link to connect an institution, then moneta sync",
		},
		{
			"missing balances",
			func(d *store.DashboardReport) { d.Networth.MissingBalance = 1 },
			"run moneta sync to pull balances for accounts with no snapshot",
		},
		{
			"healthy",
			func(d *store.DashboardReport) {},
			"run moneta spend or moneta trends for the breakdown behind these totals",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dashboard := populatedDashboard()
			test.mutate(&dashboard)
			if out := renderJSON(t, dashboard); !strings.Contains(out, `"hint":"`+test.want+`"`) {
				t.Errorf("hint = %s, want %q", out, test.want)
			}
		})
	}
}

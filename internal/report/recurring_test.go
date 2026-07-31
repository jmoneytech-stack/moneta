package report

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestRecurringRendersBigDriftPercentCanonically(t *testing.T) {
	value, ok := new(big.Int).SetString("92233720368547758060000", 10)
	if !ok {
		t.Fatal("parse big drift fixture")
	}
	recurringReport := store.RecurringReport{
		Detector: store.DetectorState{Status: "ok"},
		Items: []store.RecurringItem{{
			Name: "Boundary Example", Kind: "income", Cadence: "monthly",
			ExpectedCents: 1, DriftPercentX100: value, Drift: true,
			Active: true, Source: "detected",
		}},
	}
	var output bytes.Buffer
	if err := cli.Render(&output, Recurring(recurringReport), cli.FormatJSON); err != nil {
		t.Fatalf("render recurring boundary JSON: %v", err)
	}
	if !strings.Contains(output.String(), `"drift_pct":922337203685477580600`) {
		t.Errorf("big drift did not render canonically: %s", output.String())
	}
	if strings.Contains(output.String(), "e+") {
		t.Errorf("big drift rendered through floating point: %s", output.String())
	}
}

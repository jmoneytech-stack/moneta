package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestBillsRendersNullableMoneyAndFourFieldDetector(t *testing.T) {
	billsReport := store.BillsReport{
		AsOf: "2026-07-01", Through: "2026-07-31", Days: 30,
		Detector: store.DetectorState{
			Status: "partial", LastRunAt: "2026-07-01T12:00:00.000Z",
			LastSuccessAt:       "2026-06-01T12:00:00.000Z",
			LastSkippedOverflow: 2, LastError: "must not render",
		},
		Items: []store.BillItem{{
			Date: "2026-07-15", Name: "Card Example", AmountCents: nil,
			Source: "card_due", Kind: "bill", DateSource: "day_of_month_estimate",
			DueStatus: "upcoming",
		}},
	}
	var output bytes.Buffer
	if err := cli.Render(&output, Bills(billsReport), cli.FormatJSON); err != nil {
		t.Fatalf("render bills JSON: %v", err)
	}
	for _, want := range []string{
		`"detector":{"status":"partial","last_run_at":"2026-07-01T12:00:00.000Z","last_success_at":"2026-06-01T12:00:00.000Z","last_skipped_overflow":2}`,
		`"amount":null`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("bills JSON missing %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "must not render") {
		t.Errorf("bills detector leaked last_error: %s", output.String())
	}
}

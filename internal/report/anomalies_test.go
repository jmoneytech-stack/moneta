package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/cli"
	"github.com/jmoneytech-stack/moneta/internal/store"
)

func TestAnomaliesRendersOverflowOnlyEmptyState(t *testing.T) {
	anomalyReport := store.AnomalyReport{Period: "2026-04", SkippedOverflow: 2}
	var output bytes.Buffer
	if err := cli.Render(&output, Anomalies(anomalyReport), cli.FormatJSON); err != nil {
		t.Fatalf("render anomaly overflow JSON: %v", err)
	}
	for _, want := range []string{
		`"summary":{"period":"2026-04","count":0,"skipped_overflow":2}`,
		`"anomalies":[]`,
		`"hint":"one or more categories exceeded safe integer math and were skipped"`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("anomaly overflow JSON missing %q: %s", want, output.String())
		}
	}
}

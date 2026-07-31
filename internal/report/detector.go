package report

import (
	"github.com/jmoneytech-stack/moneta/internal/store"
	"github.com/jmoneytech-stack/moneta/internal/toon"
)

// Detector builds the shared four-field detector freshness object. Status
// surfaces may request the redacted last error as a fifth field when the
// latest detector attempt failed; dashboard and later recurring reads do not.
func Detector(state store.DetectorState, includeError bool) toon.Object {
	status := state.Status
	if status == "" {
		status = "never_run"
	}
	document := toon.Object{
		{Key: "status", Value: status},
		{Key: "last_run_at", Value: nullableDetectorTimestamp(state.LastRunAt)},
		{Key: "last_success_at", Value: nullableDetectorTimestamp(state.LastSuccessAt)},
		{Key: "last_skipped_overflow", Value: state.LastSkippedOverflow},
	}
	if includeError && status == "error" {
		document = append(document, toon.Field{Key: "last_error", Value: state.LastError})
	}
	return document
}

func nullableDetectorTimestamp(value string) any {
	if value == "" {
		return nil
	}
	return value
}

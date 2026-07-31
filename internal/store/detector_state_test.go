package store

import (
	"context"
	"testing"
)

func TestDetectorStateUpsert(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	state, err := ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() before upsert error: %v", err)
	}
	if state != (DetectorState{Status: "never_run"}) {
		t.Errorf("ReadDetectorState() before upsert = %+v, want never_run defaults", state)
	}

	// Readers remain safe if a damaged or manually edited database loses the
	// seeded singleton. The next writer restores id=1 through its upsert.
	if _, err := db.ExecContext(ctx, "DELETE FROM detector_state WHERE id = 1"); err != nil {
		t.Fatalf("delete seeded detector state: %v", err)
	}
	state, err = ReadDetectorState(ctx, db)
	if err != nil {
		t.Fatalf("ReadDetectorState() without seeded row error: %v", err)
	}
	if state != (DetectorState{Status: "never_run"}) {
		t.Errorf("ReadDetectorState() without seeded row = %+v, want never_run defaults", state)
	}

	transitions := []DetectorState{
		{
			Status:              "ok",
			LastRunAt:           "2026-08-01T12:00:00.000Z",
			LastSuccessAt:       "2026-08-01T12:00:00.000Z",
			LastSeriesCount:     3,
			LastSkippedOverflow: 1,
		},
		{
			Status:              "partial",
			LastRunAt:           "2026-08-02T12:00:00.000Z",
			LastSuccessAt:       "2026-08-01T12:00:00.000Z",
			LastSeriesCount:     4,
			LastSkippedOverflow: 2,
		},
		{
			Status:              "error",
			LastRunAt:           "2026-08-03T12:00:00.000Z",
			LastSuccessAt:       "2026-08-01T12:00:00.000Z",
			LastError:           "detector failed",
			LastSeriesCount:     4,
			LastSkippedOverflow: 2,
		},
	}
	for _, want := range transitions {
		if err := UpsertDetectorState(ctx, db, want); err != nil {
			t.Fatalf("UpsertDetectorState(%q) error: %v", want.Status, err)
		}
		got, err := ReadDetectorState(ctx, db)
		if err != nil {
			t.Fatalf("ReadDetectorState() after %q error: %v", want.Status, err)
		}
		if got != want {
			t.Errorf("ReadDetectorState() after %q = %+v, want %+v", want.Status, got, want)
		}
	}
}

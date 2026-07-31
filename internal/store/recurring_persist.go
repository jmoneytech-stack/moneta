package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoneytech-stack/moneta/internal/recurring"
)

// RecurringDetectionInput carries one pure detect result into persistence.
// SuccessfulProviderItemIDs is used only by partial positive-evidence mode.
type RecurringDetectionInput struct {
	Result                    recurring.Result
	Candidates                []recurring.Candidate
	SuccessfulProviderItemIDs map[int64]struct{}
	Complete                  bool
	RunAt                     string
}

// PersistRecurringDetection applies one detect result and its successful
// detector state atomically. It never writes transaction back-links; those are
// owned by Phase 4 PR6.
func PersistRecurringDetection(
	ctx context.Context,
	db *sql.DB,
	input RecurringDetectionInput,
) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recurring detection persistence: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	previousState, err := readDetectorState(ctx, tx)
	if err != nil {
		return err
	}
	if input.Complete {
		if err := persistCompleteRecurringDetection(ctx, tx, input); err != nil {
			return err
		}
	} else if err := persistPartialRecurringDetection(ctx, tx, input); err != nil {
		return err
	}

	state := previousState
	state.Status = "partial"
	state.LastRunAt = input.RunAt
	state.LastError = ""
	state.LastSeriesCount = len(input.Result.Series)
	state.LastSkippedOverflow = input.Result.SkippedOverflow
	if input.Complete {
		state.Status = "ok"
		state.LastSuccessAt = input.RunAt
	}
	if err := upsertDetectorState(ctx, tx, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recurring detection persistence: %w", err)
	}
	return nil
}

func persistCompleteRecurringDetection(
	ctx context.Context,
	tx *sql.Tx,
	input RecurringDetectionInput,
) error {
	emitted := make(map[recurring.Identity]struct{}, len(input.Result.Series))
	entitySet := make(map[int64]struct{})
	for _, candidate := range input.Candidates {
		entitySet[candidate.EntityID] = struct{}{}
	}
	for _, series := range input.Result.Series {
		identity := seriesIdentity(series)
		emitted[identity] = struct{}{}
		entitySet[series.EntityID] = struct{}{}
		if err := upsertCompleteRecurringSeries(ctx, tx, series); err != nil {
			return err
		}
	}
	overflow := make(map[recurring.Identity]struct{}, len(input.Result.OverflowIdentities))
	for _, identity := range input.Result.OverflowIdentities {
		overflow[identity] = struct{}{}
		entitySet[identity.EntityID] = struct{}{}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, entity_id, detect_key, amount_sign
		FROM recurring_items
		WHERE source = 'detected' AND is_active = 1
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("list active detected recurring rows: %w", err)
	}
	type activeRow struct {
		id       int64
		identity recurring.Identity
	}
	var activeRows []activeRow
	for rows.Next() {
		var row activeRow
		if err := rows.Scan(
			&row.id,
			&row.identity.EntityID,
			&row.identity.DetectKey,
			&row.identity.AmountSign,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan active detected recurring row: %w", err)
		}
		activeRows = append(activeRows, row)
		entitySet[row.identity.EntityID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list active detected recurring rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active detected recurring rows: %w", err)
	}

	for _, row := range activeRows {
		if _, inScope := entitySet[row.identity.EntityID]; !inScope {
			continue
		}
		if _, found := emitted[row.identity]; found {
			continue
		}
		if _, skipped := overflow[row.identity]; skipped {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE recurring_items
			SET is_active = 0,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ? AND source = 'detected' AND is_active = 1
		`, row.id); err != nil {
			return fmt.Errorf("deactivate unseen recurring row: %w", err)
		}
	}
	return nil
}

func upsertCompleteRecurringSeries(
	ctx context.Context,
	tx *sql.Tx,
	series recurring.Series,
) error {
	active := 0
	if series.IsActive {
		active = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO recurring_items (
			entity_id,
			name,
			kind,
			cadence,
			expected_cents,
			next_expected_date,
			drift_pct,
			source,
			is_active,
			detect_key,
			amount_sign,
			miss_count,
			last_matched_date,
			last_matched_cents,
			schedule_anchor_day
		) VALUES (?, ?, ?, ?, ?, ?, 0, 'detected', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, detect_key, amount_sign)
			WHERE source = 'detected' AND detect_key <> '' AND amount_sign IN (-1, 1)
		DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			cadence = excluded.cadence,
			expected_cents = excluded.expected_cents,
			next_expected_date = excluded.next_expected_date,
			drift_pct = 0,
			is_active = excluded.is_active,
			miss_count = excluded.miss_count,
			last_matched_date = excluded.last_matched_date,
			last_matched_cents = excluded.last_matched_cents,
			schedule_anchor_day = excluded.schedule_anchor_day,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`,
		series.EntityID,
		series.Name,
		series.Kind,
		series.Cadence,
		series.ExpectedCents,
		series.NextExpectedDate,
		active,
		series.DetectKey,
		series.AmountSign,
		series.MissCount,
		series.LastMatchedDate,
		series.LastMatchedCents,
		series.ScheduleAnchorDay,
	)
	if err != nil {
		return fmt.Errorf("upsert detected recurring row: %w", err)
	}
	return nil
}

func persistPartialRecurringDetection(
	ctx context.Context,
	tx *sql.Tx,
	input RecurringDetectionInput,
) error {
	candidatesByID := make(map[int64]recurring.Candidate, len(input.Candidates))
	for _, candidate := range input.Candidates {
		candidatesByID[candidate.TransactionID] = candidate
	}
	for _, series := range input.Result.Series {
		if len(series.MemberTransactionIDs) < 3 {
			continue
		}
		evidence, eligible := latestSuccessfulEvidence(
			series,
			candidatesByID,
			input.SuccessfulProviderItemIDs,
		)
		if !eligible {
			continue
		}
		if err := upsertPartialRecurringSeries(ctx, tx, series, evidence); err != nil {
			return err
		}
	}
	return nil
}

func latestSuccessfulEvidence(
	series recurring.Series,
	candidatesByID map[int64]recurring.Candidate,
	successfulProviderItemIDs map[int64]struct{},
) (recurring.Candidate, bool) {
	for index := len(series.MemberTransactionIDs) - 1; index >= 0; index-- {
		candidate, found := candidatesByID[series.MemberTransactionIDs[index]]
		if !found || candidate.ProviderItemID == nil {
			continue
		}
		if _, successful := successfulProviderItemIDs[*candidate.ProviderItemID]; successful {
			return candidate, true
		}
	}
	return recurring.Candidate{}, false
}

func upsertPartialRecurringSeries(
	ctx context.Context,
	tx *sql.Tx,
	series recurring.Series,
	evidence recurring.Candidate,
) error {
	var id int64
	var active, missCount int
	var lastMatchedDate sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT id, is_active, miss_count, last_matched_date
		FROM recurring_items
		WHERE source = 'detected'
		  AND entity_id = ?
		  AND detect_key = ?
		  AND amount_sign = ?
	`, series.EntityID, series.DetectKey, series.AmountSign).Scan(
		&id,
		&active,
		&missCount,
		&lastMatchedDate,
	)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read partial recurring row: %w", err)
	}
	if err == sql.ErrNoRows {
		if !series.IsActive {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO recurring_items (
				entity_id, name, kind, cadence, expected_cents,
				next_expected_date, drift_pct, source, is_active,
				detect_key, amount_sign, miss_count, last_matched_date,
				last_matched_cents, schedule_anchor_day
			) VALUES (?, ?, ?, ?, ?, ?, 0, 'detected', 1, ?, ?, ?, ?, ?, ?)
		`,
			series.EntityID,
			series.Name,
			series.Kind,
			series.Cadence,
			series.ExpectedCents,
			series.NextExpectedDate,
			series.DetectKey,
			series.AmountSign,
			series.MissCount,
			evidence.Date,
			evidence.AmountCents,
			series.ScheduleAnchorDay,
		)
		if err != nil {
			return fmt.Errorf("insert partial recurring row: %w", err)
		}
		return nil
	}

	newActive := active
	newMissCount := missCount
	if series.IsActive {
		newActive = 1
		if series.MissCount < newMissCount || active == 0 {
			newMissCount = series.MissCount
		}
	}
	matchedDate := any(nil)
	matchedCents := any(nil)
	if !lastMatchedDate.Valid || evidence.Date >= lastMatchedDate.String {
		matchedDate = evidence.Date
		matchedCents = evidence.AmountCents
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE recurring_items
		SET name = ?,
			kind = ?,
			cadence = ?,
			expected_cents = ?,
			next_expected_date = ?,
			drift_pct = 0,
			is_active = ?,
			miss_count = ?,
			last_matched_date = CASE
				WHEN ? IS NULL THEN last_matched_date ELSE ?
			END,
			last_matched_cents = CASE
				WHEN ? IS NULL THEN last_matched_cents ELSE ?
			END,
			schedule_anchor_day = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ? AND source = 'detected'
	`,
		series.Name,
		series.Kind,
		series.Cadence,
		series.ExpectedCents,
		series.NextExpectedDate,
		newActive,
		newMissCount,
		matchedDate,
		matchedDate,
		matchedCents,
		matchedCents,
		series.ScheduleAnchorDay,
		id,
	)
	if err != nil {
		return fmt.Errorf("update partial recurring row: %w", err)
	}
	return nil
}

func seriesIdentity(series recurring.Series) recurring.Identity {
	return recurring.Identity{
		EntityID:   series.EntityID,
		DetectKey:  series.DetectKey,
		AmountSign: series.AmountSign,
	}
}

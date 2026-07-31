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
	AsOf                      string
}

// PersistRecurringDetection applies one detect result, its transaction
// back-links, and its successful detector state atomically. AsOf defines the
// complete-mode lookback boundary used by unlinking.
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
	emittedIDs := make(map[recurring.Identity]int64, len(input.Result.Series))
	entitySet := make(map[int64]struct{})
	for _, candidate := range input.Candidates {
		entitySet[candidate.EntityID] = struct{}{}
	}
	for _, series := range input.Result.Series {
		identity := seriesIdentity(series)
		emitted[identity] = struct{}{}
		entitySet[series.EntityID] = struct{}{}
		id, err := upsertCompleteRecurringSeries(ctx, tx, series)
		if err != nil {
			return err
		}
		emittedIDs[identity] = id
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
	if err := backLinkCompleteRecurringSeries(ctx, tx, input, emittedIDs); err != nil {
		return err
	}
	return nil
}

func upsertCompleteRecurringSeries(
	ctx context.Context,
	tx *sql.Tx,
	series recurring.Series,
) (int64, error) {
	active := 0
	if series.IsActive {
		active = 1
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
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
		RETURNING id
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
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert detected recurring row: %w", err)
	}
	return id, nil
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
	type partialLink struct {
		seriesID int64
		series   recurring.Series
	}
	var links []partialLink
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
		seriesID, persisted, err := upsertPartialRecurringSeries(ctx, tx, series, evidence)
		if err != nil {
			return err
		}
		if persisted {
			links = append(links, partialLink{seriesID: seriesID, series: series})
		}
	}
	for _, link := range links {
		if err := backLinkPartialRecurringSeries(
			ctx,
			tx,
			link.seriesID,
			link.series,
			candidatesByID,
			input.SuccessfulProviderItemIDs,
		); err != nil {
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
) (int64, bool, error) {
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
		return 0, false, fmt.Errorf("read partial recurring row: %w", err)
	}
	if err == sql.ErrNoRows {
		if !series.IsActive {
			return 0, false, nil
		}
		err := tx.QueryRowContext(ctx, `
			INSERT INTO recurring_items (
				entity_id, name, kind, cadence, expected_cents,
				next_expected_date, drift_pct, source, is_active,
				detect_key, amount_sign, miss_count, last_matched_date,
				last_matched_cents, schedule_anchor_day
			) VALUES (?, ?, ?, ?, ?, ?, 0, 'detected', 1, ?, ?, ?, ?, ?, ?)
			RETURNING id
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
		).Scan(&id)
		if err != nil {
			return 0, false, fmt.Errorf("insert partial recurring row: %w", err)
		}
		return id, true, nil
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
		return 0, false, fmt.Errorf("update partial recurring row: %w", err)
	}
	return id, true, nil
}

func backLinkCompleteRecurringSeries(
	ctx context.Context,
	tx *sql.Tx,
	input RecurringDetectionInput,
	emittedIDs map[recurring.Identity]int64,
) error {
	if input.AsOf == "" || len(input.Result.Series) == 0 {
		return nil
	}
	lookbackStart, lookbackEnd, err := recurring.Lookback(input.AsOf)
	if err != nil {
		return err
	}
	candidatesByID := make(map[int64]recurring.Candidate, len(input.Candidates))
	for _, candidate := range input.Candidates {
		candidatesByID[candidate.TransactionID] = candidate
	}
	type linkTarget struct {
		seriesID int64
		members  []int64
	}
	targets := make([]linkTarget, 0, len(input.Result.Series))
	allEmittedMembers := make(map[int64]struct{})
	for _, series := range input.Result.Series {
		seriesID, found := emittedIDs[seriesIdentity(series)]
		if !found {
			continue
		}
		members := make([]int64, 0, len(series.MemberTransactionIDs))
		for _, transactionID := range series.MemberTransactionIDs {
			candidate, found := candidatesByID[transactionID]
			if !found || candidate.EntityID != series.EntityID ||
				candidate.Date < lookbackStart || candidate.Date > lookbackEnd {
				continue
			}
			members = append(members, transactionID)
			allEmittedMembers[transactionID] = struct{}{}
		}
		targets = append(targets, linkTarget{seriesID: seriesID, members: members})
	}

	// Re-key emitted members only from detected rows that are now inactive.
	// Manual and other emitted active targets are deliberately ineligible.
	for _, target := range targets {
		for _, transactionID := range target.members {
			if _, err := tx.ExecContext(ctx, `
				UPDATE transactions
				SET recurring_id = ?,
					updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				WHERE id = ?
				  AND recurring_id IN (
					SELECT id FROM recurring_items
					WHERE source = 'detected' AND is_active = 0
				  )
			`, target.seriesID, transactionID); err != nil {
				return fmt.Errorf("re-key recurring transaction: %w", err)
			}
		}
	}
	// Link remaining members only when unclaimed or already linked here.
	for _, target := range targets {
		for _, transactionID := range target.members {
			if _, err := tx.ExecContext(ctx, `
				UPDATE transactions
				SET recurring_id = ?,
					updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				WHERE id = ? AND (recurring_id IS NULL OR recurring_id = ?)
			`, target.seriesID, transactionID, target.seriesID); err != nil {
				return fmt.Errorf("link recurring transaction: %w", err)
			}
		}
	}
	// Unlink only lookback rows that still point at this emitted series but are
	// no longer members. Pre-lookback history and unrelated ended series stay.
	for _, target := range targets {
		memberSet := make(map[int64]struct{}, len(target.members))
		for _, transactionID := range target.members {
			memberSet[transactionID] = struct{}{}
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id FROM transactions
			WHERE recurring_id = ? AND date >= ? AND date <= ?
			ORDER BY id
		`, target.seriesID, lookbackStart, lookbackEnd)
		if err != nil {
			return fmt.Errorf("list recurring lookback links: %w", err)
		}
		var unlinkIDs []int64
		for rows.Next() {
			var transactionID int64
			if err := rows.Scan(&transactionID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan recurring lookback link: %w", err)
			}
			if _, member := memberSet[transactionID]; member {
				continue
			}
			if _, emittedElsewhere := allEmittedMembers[transactionID]; emittedElsewhere {
				continue
			}
			unlinkIDs = append(unlinkIDs, transactionID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("list recurring lookback links: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close recurring lookback links: %w", err)
		}
		for _, transactionID := range unlinkIDs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE transactions
				SET recurring_id = NULL,
					updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
				WHERE id = ? AND recurring_id = ?
			`, transactionID, target.seriesID); err != nil {
				return fmt.Errorf("unlink recurring lookback non-member: %w", err)
			}
		}
	}
	return nil
}

func backLinkPartialRecurringSeries(
	ctx context.Context,
	tx *sql.Tx,
	seriesID int64,
	series recurring.Series,
	candidatesByID map[int64]recurring.Candidate,
	successfulProviderItemIDs map[int64]struct{},
) error {
	for _, transactionID := range series.MemberTransactionIDs {
		candidate, found := candidatesByID[transactionID]
		if !found || candidate.EntityID != series.EntityID || candidate.ProviderItemID == nil {
			continue
		}
		if _, successful := successfulProviderItemIDs[*candidate.ProviderItemID]; !successful {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE transactions
			SET recurring_id = ?,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE id = ? AND (recurring_id IS NULL OR recurring_id = ?)
		`, seriesID, transactionID, seriesID); err != nil {
			return fmt.Errorf("link partial recurring transaction: %w", err)
		}
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

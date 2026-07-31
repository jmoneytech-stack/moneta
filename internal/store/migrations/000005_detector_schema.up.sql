ALTER TABLE recurring_items
    ADD COLUMN detect_key TEXT NOT NULL DEFAULT '';

ALTER TABLE recurring_items
    ADD COLUMN amount_sign INTEGER NOT NULL DEFAULT 0
        CHECK (amount_sign IN (-1, 0, 1));

ALTER TABLE recurring_items
    ADD COLUMN miss_count INTEGER NOT NULL DEFAULT 0
        CHECK (miss_count >= 0);

ALTER TABLE recurring_items
    ADD COLUMN last_matched_date TEXT CHECK (
        last_matched_date IS NULL OR (
            length(last_matched_date) = 10 AND
            last_matched_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
        )
    );

ALTER TABLE recurring_items
    ADD COLUMN last_matched_cents INTEGER;

ALTER TABLE recurring_items
    ADD COLUMN schedule_anchor_day INTEGER
        CHECK (schedule_anchor_day BETWEEN 1 AND 31);

CREATE UNIQUE INDEX recurring_items_detected_identity_uidx
    ON recurring_items (entity_id, detect_key, amount_sign)
    WHERE source = 'detected' AND detect_key <> '' AND amount_sign IN (-1, 1);

CREATE TABLE detector_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    status TEXT NOT NULL DEFAULT 'never_run'
        CHECK (status IN ('never_run', 'ok', 'error', 'partial')),
    last_run_at TEXT,
    last_success_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    last_series_count INTEGER NOT NULL DEFAULT 0,
    last_skipped_overflow INTEGER NOT NULL DEFAULT 0
) STRICT;

INSERT INTO detector_state (id, status) VALUES (1, 'never_run');

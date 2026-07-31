DROP INDEX recurring_items_detected_identity_uidx;

DROP TABLE detector_state;

ALTER TABLE recurring_items
    DROP COLUMN schedule_anchor_day;

ALTER TABLE recurring_items
    DROP COLUMN last_matched_cents;

ALTER TABLE recurring_items
    DROP COLUMN last_matched_date;

ALTER TABLE recurring_items
    DROP COLUMN miss_count;

ALTER TABLE recurring_items
    DROP COLUMN amount_sign;

ALTER TABLE recurring_items
    DROP COLUMN detect_key;

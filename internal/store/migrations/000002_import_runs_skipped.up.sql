ALTER TABLE import_runs
    ADD COLUMN skipped INTEGER NOT NULL DEFAULT 0 CHECK (skipped >= 0);

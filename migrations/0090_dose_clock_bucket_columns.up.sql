-- 0090_dose_clock_bucket_columns.up.sql — CHO-2087 dose-clock accelerator.
--
-- The dose-day clock became env-tunable (CHORA_DOSE_DAY_SECONDS — owner-
-- directed accelerated E2E testing; default stays real UTC calendar days).
-- The two day-bucket columns were DATE, which silently collapses every
-- sub-day bucket of one calendar day into the same value — breaking the
-- D13 per-bucket qgen budget count and the D7 per-bucket pacing stamp at
-- accelerated speed. timestamptz holds both speeds; at product speed the
-- stored value is the same UTC midnight the DATE held (value-compatible,
-- no backfill needed beyond the type cast).
ALTER TABLE campaign_question_sets
    ALTER COLUMN requested_on TYPE timestamptz
    USING (requested_on::timestamp AT TIME ZONE 'UTC');

COMMENT ON COLUMN campaign_question_sets.requested_on IS
    'D13 generation-budget bucket start (doseclock; UTC midnight at product per-day speed, sub-day at accelerated test speed — mig 0090).';

ALTER TABLE campaign_node_progress
    ALTER COLUMN last_advance_date TYPE timestamptz
    USING (last_advance_date::timestamp AT TIME ZONE 'UTC');

COMMENT ON COLUMN campaign_node_progress.last_advance_date IS
    'D7 pacing-gate bucket start (doseclock; UTC midnight at product per-day speed, sub-day at accelerated test speed — mig 0090).';

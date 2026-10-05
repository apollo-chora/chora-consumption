-- =============================================================================
-- chora-consumption : 0099_student_transcript_delivery_type.down.sql
--                     (reverse of .up.sql)
--
-- CHO-2224 — drop the transcript's mode-attribution column.
--
-- Data loss is acceptable and bounded: delivery_type is an additive, nullable
-- DISPLAY attribute with no FK, no index and no policy depending on it. It is a
-- pure PROJECTION of a snapshot carried on chora.delivery.submission.graded.v1
-- field 14, so every value is reproducible by replaying the graded events; no
-- fact originates here.
--
-- Reverting this column reverts §10.6 capstone criterion 1: the transcript goes
-- back to attributing 2 KINDS and never 2 MODES.
-- =============================================================================

BEGIN;

ALTER TABLE student_transcript_entries
    DROP COLUMN IF EXISTS delivery_type;

COMMIT;

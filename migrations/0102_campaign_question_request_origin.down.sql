-- =============================================================================
-- chora-consumption : 0102_campaign_question_request_origin.down.sql
--                     (reverse of .up.sql)
--
-- CHO-2298: drop the campaign qgen daily-budget origin axis.
--
-- Data loss is bounded and acceptable: request_origin is an additive column
-- with no FK, no index and no policy depending on it, and every value is
-- re-derivable (the sole requester restamps it on the next request). Reverting
-- collapses the per-origin split back to a single shared daily cap.
-- =============================================================================

BEGIN;

ALTER TABLE campaign_question_sets
    DROP COLUMN IF EXISTS request_origin;

COMMIT;

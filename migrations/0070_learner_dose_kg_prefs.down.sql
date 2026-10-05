-- =============================================================================
-- chora-consumption : 0070_learner_dose_kg_prefs.down.sql
--
-- Inverse of 0070_learner_dose_kg_prefs.up.sql — drops the table and all its
-- indexes. The RLS policy auto-drops with the table.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS learner_dose_kg_prefs;

COMMIT;

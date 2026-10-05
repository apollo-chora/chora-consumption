-- =============================================================================
-- chora-consumption : 0104_concept_sub_goal.down.sql
--
-- Reverse 0104: drop the per-node sub_goal columns (ADR-247 D1, CHO-2328).
-- Idempotent (DROP COLUMN IF EXISTS). RLS/grants unchanged.
-- =============================================================================

BEGIN;

ALTER TABLE concept_nodes
    DROP COLUMN IF EXISTS sub_goal,
    DROP COLUMN IF EXISTS sub_goal_provenance;

COMMIT;

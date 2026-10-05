-- =============================================================================
-- chora-consumption : 0052_goal_mastered_concept_count.down.sql
-- Reverts 0052: drop the goal-graduation subscriber's mastered-count high-water
-- mark. The %-ring read path derives progress live, so dropping this is safe.
-- =============================================================================

BEGIN;

ALTER TABLE goals
    DROP COLUMN IF EXISTS mastered_concept_count;

COMMIT;

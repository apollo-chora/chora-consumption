-- =============================================================================
-- chora-consumption : 0098_diagnose_goal_scope.down.sql  (reverse of .up.sql)
-- ADR-238 — drop the goal-scope columns + index. Data loss on the two nullable
-- columns is acceptable (additive, no downstream FK).
-- =============================================================================

BEGIN;

DROP INDEX IF EXISTS idx_learner_weakness_target_concept;

ALTER TABLE learner_weakness
    DROP COLUMN IF EXISTS target_concept_id;

ALTER TABLE weakness_doc_uploads
    DROP COLUMN IF EXISTS goal_id;

COMMIT;

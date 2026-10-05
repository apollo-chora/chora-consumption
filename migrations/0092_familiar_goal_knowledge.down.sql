-- chora-consumption : 0092_familiar_goal_knowledge.down.sql
-- Reverses 0092. familiar_goal_knowledge is a DERIVED read-model cache — every
-- row is reconstructible by re-synthesising from its sources
-- (familiar_memory_recall + goal subtree + learner_weakness), so dropping it
-- loses no source-of-truth data. The soft-delete invariant governs LEARNER data
-- in live tables; it does not oblige a rollback to keep a projection table.
--
-- The enums are dropped only if nothing else references them (a later migration
-- may have reused them), so this stays safe to re-run.

BEGIN;

DROP TABLE IF EXISTS familiar_goal_knowledge;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'familiar_goal_knowledge_invalidation_reason')
       AND NOT EXISTS (
           SELECT 1 FROM pg_attribute a
           JOIN pg_type t ON a.atttypid = t.oid
           WHERE t.typname = 'familiar_goal_knowledge_invalidation_reason' AND NOT a.attisdropped
       ) THEN
        DROP TYPE familiar_goal_knowledge_invalidation_reason;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'familiar_goal_knowledge_status')
       AND NOT EXISTS (
           SELECT 1 FROM pg_attribute a
           JOIN pg_type t ON a.atttypid = t.oid
           WHERE t.typname = 'familiar_goal_knowledge_status' AND NOT a.attisdropped
       ) THEN
        DROP TYPE familiar_goal_knowledge_status;
    END IF;
END$$;

COMMIT;

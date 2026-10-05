-- chora-consumption : 0095_goal_knowledge_silent_status.down.sql
-- Reverse of 0095 (CHO-2180 terminal 'silent' status).
--
-- PostgreSQL cannot DROP a value from an enum. Reversing this honestly means
-- rebuilding the type without 'silent' — and any row already carrying it must be
-- resolved FIRST, because there is nowhere in the old vocabulary for it to land.
--
-- A silent row is demoted to 'pending' with its stamp cleared, which is precisely
-- the pre-0095 representation of a declined synthesis: never-generated, and
-- therefore re-requested on the next read. That deliberately restores CHO-2180's
-- behaviour (the learner sees "reflecting…" again and the doomed call is re-bought)
-- — because that IS what "before 0095" means. Down-migrating this is a rollback,
-- not a fix.

ALTER TABLE familiar_goal_knowledge
    ALTER COLUMN status DROP DEFAULT;

UPDATE familiar_goal_knowledge
   SET status                = 'pending',
       generated_at          = NULL,
       generated_by_run_id   = NULL,
       generated_by_model_id = NULL,
       prompt_version        = NULL,
       updated_at            = NOW()
 WHERE status = 'silent';

ALTER TYPE familiar_goal_knowledge_status RENAME TO familiar_goal_knowledge_status_old;

CREATE TYPE familiar_goal_knowledge_status AS ENUM ('pending', 'fresh', 'stale');

ALTER TABLE familiar_goal_knowledge
    ALTER COLUMN status TYPE familiar_goal_knowledge_status
    USING status::text::familiar_goal_knowledge_status;

ALTER TABLE familiar_goal_knowledge
    ALTER COLUMN status SET DEFAULT 'pending';

DROP TYPE familiar_goal_knowledge_status_old;

COMMENT ON COLUMN familiar_goal_knowledge.status IS
    'Cache lifecycle: pending = never generated or a synthesis is in flight; fresh = synthesised and not invalidated; stale = invalidated but KEEPS its last good text (serve-stale-while-regen).';

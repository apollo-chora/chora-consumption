-- 0043 — course-bound LearningPath durability (R3 / OPEN-1)
-- -----------------------------------------------------------------------------
-- Adds course_id + enrollment_id + current_index to learning_paths so the
-- pg-backed LearningPathRepo can serve the course-scoped open-course read
-- (GET /v1/me/learning-paths?course_id=X) durably across pod restarts and
-- replicas, replacing the in-memory adapter that lost bootstrapped paths on
-- every restart (the binding cause of the 404 PATH_NOT_BOOTSTRAPPED seen in
-- the CJ#2 Stage-F smoke).
--
-- Also drops the `cardinality(atom_ids) >= 1` CHECK from 0001 so a PUBLISHED
-- course with no atoms yet (test-set-only, or atoms not yet projected) can
-- bootstrap a 0-atom path (OPEN-1). Atoms fill in via LearningPath.AppendAtom
-- as chora.creation.atom.created.v1 events arrive.

ALTER TABLE learning_paths ADD COLUMN IF NOT EXISTS course_id     UUID NULL;
ALTER TABLE learning_paths ADD COLUMN IF NOT EXISTS enrollment_id UUID NULL;
ALTER TABLE learning_paths ADD COLUMN IF NOT EXISTS current_index INTEGER NOT NULL DEFAULT 0;

-- Drop the >= 1 atom cardinality CHECK (the constraint name is auto-generated,
-- so resolve it by definition). 0-atom paths are now valid.
DO $$
DECLARE c text;
BEGIN
  SELECT conname INTO c FROM pg_constraint
   WHERE conrelid = 'learning_paths'::regclass AND contype = 'c'
     AND pg_get_constraintdef(oid) ILIKE '%cardinality%atom_ids%';
  IF c IS NOT NULL THEN
    EXECUTE format('ALTER TABLE learning_paths DROP CONSTRAINT %I', c);
  END IF;
END $$;

-- One course-bound path per (tenant, course, learner). Backs the idempotent
-- GetByCourseAndGCID lookup used by the enrollment-bootstrap subscriber and
-- the open-course read. Partial: legacy non-course paths (course_id NULL) are
-- unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS idx_learning_paths_course_owner
  ON learning_paths (tenant_id, course_id, owner_gcid)
  WHERE course_id IS NOT NULL AND deleted_at IS NULL;

-- =============================================================================
-- chora-consumption : 0038_fam1_reshape_phyllis_roster.down.sql
--
-- Rolls back the FAM-1 roster reshape inserted by
-- 0038_fam1_reshape_phyllis_roster.up.sql.
--
-- Soft-delete approach per .claude/rules/ddd-enforcement.md §Soft Deletes —
-- we set deleted_at on the 3 new familiar_instances (Aria/Mystery Egg/Ignis)
-- AND undo the soft-delete of the 3 old familiars (Pythagoras/Kepler/Galileo)
-- so the pre-FAM-1 demo state is restored. Down therefore swaps the roster
-- back to (Pythagoras, Kepler, Galileo, Eira).
--
-- Idempotent — re-running converges to the same end state (old visible,
-- new hidden).
-- =============================================================================

BEGIN;

SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';

-- Mirror the trigger-disable workaround so the trg_familiar_instances_updated_at
-- trigger does not abort the rollback (same `version`-column drift).
ALTER TABLE familiar_instances DISABLE TRIGGER trg_familiar_instances_updated_at;

-- 1) Soft-delete the 3 NEW (FAM-1) familiars.
UPDATE familiar_instances
   SET deleted_at = COALESCE(deleted_at, now())
 WHERE familiar_id IN (
       '00000000-0000-7000-8000-00000000f1a1'::uuid,  -- Aria
       '00000000-0000-7000-8000-00000000f1a2'::uuid,  -- Mystery Egg
       '00000000-0000-7000-8000-00000000f1a3'::uuid   -- Ignis
       );

-- 2) Restore (undelete) the 3 OLD familiars that 0038.up soft-deleted.
UPDATE familiar_instances
   SET deleted_at = NULL
 WHERE familiar_id IN (
       '00000000-0000-7000-8000-00000000f101'::uuid,  -- Pythagoras
       '00000000-0000-7000-8000-00000000f102'::uuid,  -- Kepler
       '00000000-0000-7000-8000-00000000f103'::uuid   -- Galileo
       );

ALTER TABLE familiar_instances ENABLE TRIGGER trg_familiar_instances_updated_at;

COMMIT;

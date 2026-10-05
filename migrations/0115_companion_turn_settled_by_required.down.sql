-- =============================================================================
-- chora-consumption : 0115_companion_turn_settled_by_required.down.sql
--
-- Drops the constraint 0115 raised.
--
-- ⚠ THIS DOWN HAS ONE LEGITIMATE USE AND IT IS TIME-CRITICAL.
--   Run it BEFORE rolling chora-consumption back below digest 75333a903944
--   (the image that stamps settled_by). The old binary completes a turn with an
--   UPDATE that never sets settled_by, so with this constraint in place EVERY
--   completion on the companion and dose lanes would be rejected — a total lane
--   outage caused by a schema object rather than by any shipped code.
--
--   The up-migration explains why the constraint is not written to tolerate
--   that rollback: every formulation that tolerates the old binary is simply
--   the absence of the invariant. The ordering IS the mitigation, so this file
--   is that mitigation and it must run FIRST, not after the symptom.
--
--   The settled_by and late_completion_at COLUMNS are left in place: they are
--   additive and harmless to the old binary, and 0114 owns them. Dropping them
--   is 0114's DOWN, not this one's.
-- =============================================================================

BEGIN;

ALTER TABLE companion_turns
  DROP CONSTRAINT IF EXISTS companion_turns_settled_by_required;

COMMIT;

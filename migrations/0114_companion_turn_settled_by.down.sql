-- =============================================================================
-- chora-consumption : 0114_companion_turn_settled_by.down.sql
--
-- Reverses 0114. Drops the three constraints first (a column cannot be dropped
-- while a CHECK references it), then the two columns.
--
-- No value rewrite here, so unlike 0110 there is nothing for FORCE ROW LEVEL
-- SECURITY to silently filter: DROP CONSTRAINT and DROP COLUMN are catalog
-- operations and are not RLS-filtered. The lift is deliberately absent rather
-- than forgotten.
--
-- ⚠ This is lossy by nature: settled_by and late_completion_at are the only
-- record that a turn was reaped rather than answered, and that a late answer
-- ever arrived. Dropping them does not restore the old behaviour, it destroys
-- the evidence the old behaviour never captured. Run only to unwind a bad
-- deploy, never to "clean up".
-- =============================================================================

BEGIN;

SET LOCAL lock_timeout = '30s';

ALTER TABLE companion_turns DROP CONSTRAINT IF EXISTS companion_turns_late_completion_needs_terminal;
ALTER TABLE companion_turns DROP CONSTRAINT IF EXISTS companion_turns_terminal_has_settled_by;  -- harmless if the follow-up migration added it
ALTER TABLE companion_turns DROP CONSTRAINT IF EXISTS companion_turns_settled_by_valid;

ALTER TABLE companion_turns DROP COLUMN IF EXISTS late_completion_at;
ALTER TABLE companion_turns DROP COLUMN IF EXISTS settled_by;

COMMIT;

-- =============================================================================
-- chora-consumption : 0120_companion_ritual_suspended_status.down.sql
--
-- Reverts 0120: narrows the status CHECK back to the five terminals 0071 knew
-- and drops paused_companion_id.
--
-- THIS REVERT CAN FAIL, AND THAT IS CORRECT.
-- If any run has already terminated as 'companion_suspended', re-adding the
-- narrow CHECK will refuse with 23514 and the whole transaction rolls back. The
-- migration deliberately does NOT rewrite those rows to 'failed' to get itself
-- through: that would relabel an operator's deliberate pause as a product fault,
-- in an append-only audit table, to make a revert convenient. A revert that
-- stops and says which rows are in the way is the honest outcome.
--
-- To revert deliberately, decide what those runs should say and rewrite them
-- yourself first. There is no correct automatic answer.
--
-- Dropping paused_companion_id loses which companion was paused. The terminal
-- itself is what the status column held, so a re-apply cannot recover the id.
-- =============================================================================

BEGIN;

ALTER TABLE companion_ritual_runs
    DROP COLUMN IF EXISTS paused_companion_id;

ALTER TABLE companion_ritual_runs
    DROP CONSTRAINT IF EXISTS companion_ritual_runs_status_check;

ALTER TABLE companion_ritual_runs
    ADD CONSTRAINT companion_ritual_runs_status_check
    CHECK (status IN ('running','completed','failed','skipped_budget','blocked'));

COMMIT;

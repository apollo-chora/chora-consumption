-- =============================================================================
-- chora-consumption : 0118_student_transcript_seen_at.down.sql
--
-- Reverts 0118. Dropping the column DESTROYS every first-seen stamp, and there
-- is no way to reconstruct one: nothing else records when a learner opened a
-- result. After a revert and a re-apply, every result reads as unopened again.
--
-- That is stated rather than hidden because it is the honest cost of the
-- revert, and it is acceptable only because the flag is a display signal, not
-- an obligation: no grade, no certification and no audit row depends on it.
-- =============================================================================

BEGIN;

DROP INDEX IF EXISTS idx_student_transcript_entries_unseen;

ALTER TABLE student_transcript_entries
    DROP COLUMN IF EXISTS seen_at;

COMMIT;

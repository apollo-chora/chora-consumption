-- =============================================================================
-- chora-consumption : 0119_companion_ritual_step_outputs.down.sql
--
-- Reverts 0119. Dropping the column DESTROYS every stored step output, and
-- nothing else recorded them: the runner's buffer is gone with the process and
-- the sink holds only what the completed run wrote there, which is nothing at
-- all for a run that stopped early. After a revert and a re-apply, every past
-- run's story falls back to the fixed "Your companion used {name}." sentence
-- and no run can say where it stopped.
--
-- That is stated rather than hidden because it is the honest cost of the revert.
-- It is acceptable only because the column is an EXPLANATION of a run, not the
-- run: the ADR-197 decision stamps, the mana charge, the sink reference and the
-- terminal status all live in their own columns and survive untouched, so no
-- audit fact and no pricing fact depends on this one.
-- =============================================================================

BEGIN;

ALTER TABLE companion_ritual_runs
    DROP COLUMN IF EXISTS step_outputs;

COMMIT;

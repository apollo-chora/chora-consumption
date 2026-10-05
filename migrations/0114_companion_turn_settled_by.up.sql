-- =============================================================================
-- chora-consumption : 0114_companion_turn_settled_by.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : AUDIT-G5. Nothing reaps an expired turn, and nothing records a
--            result that arrives after one. This migration adds the two facts
--            that make a reaped turn distinguishable from an answered one.
--
-- 0114 COMPLETES A DESIGN, IT DOES NOT INVENT ONE
-- -----------------------------------------------
-- 0111 already anticipated a reaper: `timeout` is in the status CHECK, and
-- idx_companion_turns_open (tenant_id, deadline_at) WHERE status = 'accepted'
-- is an index whose only purpose is finding expired open turns cheaply. The
-- schema was ready; only the actor was missing, and with it the vocabulary to
-- say WHAT settled a turn.
--
-- WHY status IS NOT ENOUGH
-- ------------------------
-- A reaped turn and an answered turn are BOTH terminal and BOTH carry a
-- completed_at. The audit gate reads "did this lane answer", and status cannot
-- carry that. settled_by can. The vocabulary deliberately mirrors the kernel's
-- ai_kernel_agent_dispatch_parks.settled_by: the two ledgers describe the same
-- event from either end and should read alike.
--
-- late_completion_at exists for a real case, not a hypothetical: reaped at the
-- deadline, answered a second later. Before this, turn.go's docstring claimed
-- the store "records it as late", while companion_turn_repo.go caught
-- ErrTurnAlreadyTerminal and returned nil. The answer was ACKed and discarded
-- and the claimed control did not exist. A docstring is not a control.
--
-- ⚠ THE BACKFILL LIFTS FORCE RLS, AND MUST
-- ----------------------------------------
-- companion_turns is FORCE ROW LEVEL SECURITY. This migration runs as
-- chora_consumption_migrate, which OWNS the table, and an owner is NOT exempt
-- from FORCE: with no tenant GUC every policy evaluates false, so the backfill
-- below would match ZERO rows and silently no-op, while the ADD CONSTRAINT
-- would still validate EVERY row and fail on the rows the backfill never
-- touched. THE WRITE IS FILTERED AND THE CHECK IS NOT. That is the 0110 defect
-- exactly, and it is why the lift is here rather than trusted to be unnecessary.
-- =============================================================================

BEGIN;

SET LOCAL lock_timeout = '30s';

ALTER TABLE companion_turns ADD COLUMN IF NOT EXISTS settled_by        TEXT;
ALTER TABLE companion_turns ADD COLUMN IF NOT EXISTS late_completion_at TIMESTAMPTZ;

COMMENT ON COLUMN companion_turns.settled_by IS
  'AUDIT-G5: WHAT made this turn terminal. completion = an answerer spoke (including FAILED and REJECTED results: the question is whether anyone answered, not whether it succeeded). timeout = the deadline passed and the turn was reaped, so no answerer ever spoke. NULL while accepted. Mirrors ai_kernel_agent_dispatch_parks.settled_by.';

COMMENT ON COLUMN companion_turns.late_completion_at IS
  'AUDIT-G5: a real result arrived AFTER this turn was already terminal. The status never flips back (the caller was answered with a timeout and that history stays true); this is evidence that the lane DID eventually answer, which is a different fact from never answering. NULL is the normal case.';

-- Backfill from what status already tells us. Terminal rows that carry a result
-- were settled by an answerer; rows in 'timeout' were reaped.
DO $$
DECLARE
  was_forced boolean;
BEGIN
  SELECT relforcerowsecurity INTO was_forced
    FROM pg_class WHERE oid = 'public.companion_turns'::regclass;

  IF was_forced THEN
    EXECUTE 'ALTER TABLE companion_turns NO FORCE ROW LEVEL SECURITY';
  END IF;

  UPDATE companion_turns
     SET settled_by = 'timeout'
   WHERE status = 'timeout' AND settled_by IS NULL;

  UPDATE companion_turns
     SET settled_by = 'completion'
   WHERE status IN ('completed', 'failed', 'rejected') AND settled_by IS NULL;

  IF was_forced THEN
    EXECUTE 'ALTER TABLE companion_turns FORCE ROW LEVEL SECURITY';
  END IF;
END $$;

-- Fail loud rather than commit a table left owner-readable.
DO $$
DECLARE
  forced boolean;
BEGIN
  SELECT relforcerowsecurity INTO forced
    FROM pg_class WHERE oid = 'public.companion_turns'::regclass;
  IF NOT forced THEN
    RAISE EXCEPTION '0114: companion_turns left without FORCE ROW LEVEL SECURITY';
  END IF;
END $$;

-- Enforced only now, after the backfill has actually reached the rows.
ALTER TABLE companion_turns
  ADD CONSTRAINT companion_turns_settled_by_valid
  CHECK (settled_by IS NULL OR settled_by IN ('completion', 'timeout'));

-- ⚠ DELIBERATELY ABSENT, AND THIS IS A DEPLOY-ORDERING DECISION, NOT AN
-- OVERSIGHT: `CHECK (status = 'accepted' OR settled_by IS NOT NULL)`.
--
-- Migrations land BEFORE the binary that writes the new column. The currently
-- deployed consumption image completes a turn with an UPDATE that does not set
-- settled_by, so that constraint would reject EVERY completion the moment this
-- migration applied, and the companion and dose lanes would start failing on a
-- schema change alone. Additive columns are safe against an old binary;
-- a NOT-NULL-shaped CHECK over a column the old binary never writes is not.
--
-- It belongs in a follow-up migration, run only once the image that stamps
-- settled_by is confirmed live. Until then settled_by is populated by the new
-- code and backfilled for history, and the absence of the constraint is the
-- price of not breaking the running service.

-- A late completion only makes sense once the turn is already terminal.
ALTER TABLE companion_turns
  ADD CONSTRAINT companion_turns_late_completion_needs_terminal
  CHECK (late_completion_at IS NULL OR status <> 'accepted');

COMMIT;

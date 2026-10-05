-- =============================================================================
-- chora-consumption : 0115_companion_turn_settled_by_required.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : AUDIT-G5 follow-up. Raises the constraint 0114 DELIBERATELY
--            DEFERRED: a terminal turn must say what settled it.
--
-- WHY THIS IS A SEPARATE MIGRATION
-- --------------------------------
-- 0114 added settled_by and explained, in the file, why the NOT-NULL-shaped
-- CHECK could not ship with it: migrations land BEFORE binaries and auto-apply
-- on push, and the image deployed at the time completed a turn with an UPDATE
-- that never set settled_by. Shipping the constraint then would have rejected
-- EVERY completion on a schema change alone. Additive columns are safe against
-- an old binary; a NOT-NULL-shaped CHECK over a column that binary never writes
-- is not.
--
-- THE GATE THAT LET THIS RUN, AND THE ONE THAT WAS NOT ENOUGH
-- -----------------------------------------------------------
-- ⚠ THE ORIGINAL GATE WAS INVALID, AND THIS IS THE LESSON THE FILE EXISTS TO
-- CARRY. It was "the image that stamps settled_by is CONFIRMED SERVING",
-- satisfied 2026-08-23T13:17Z by chora-consumption digest
-- sha256:75333a903944a9c49a684243acef9928d44618cda3bebeef77170d1e1c8189ad,
-- spec == running read BY CONTAINER NAME, and verified BY CONTENT: the
-- `settled_by` and `late_completion_at` symbols were counted inside the
-- extracted binary against a deliberately fake control symbol returning 0, so
-- the check genuinely discriminated.
--
-- That check was sound and it proved the wrong thing. IT PROVED THE CODE
-- WRITES THE COLUMN. IT DID NOT PROVE THE COLUMN EXISTS. At 13:17Z it did not:
-- migration 0114 had been committed but never applied (the CI migrations lane
-- had been refusing to stage for a month, on an unrelated destructive file in
-- chora-sharing). So the running binary wrote a column the database did not
-- have, and this constraint — raised on that gate — would have failed on a
-- missing column. Different instruments answer different questions; a symbol
-- in a binary and a column in a catalogue are not the same claim.
--
-- THE GATE THAT ACTUALLY HOLDS, and the only one to use for a migration that
-- depends on a prior migration: a DIRECT information_schema READ, with
-- controls. Satisfied 2026-08-23T16:19Z after build
-- 813e0c46-fadf-4685-a51a-f79474c78de4 applied 0114 at 16:18:58Z:
--     settled_by         text                      exists
--     late_completion_at timestamp with time zone  exists
--     POSITIVE CONTROL  'status'    = 1   (the query can see columns)
--     NEGATIVE CONTROL  fake column = 0   (it discriminates)
--     both 0114 constraints present in pg_constraint
--
-- ⚠ AND NOT FROM THE JOB'S OWN REPORT. That build's overall status is FAILURE
-- (a later `verify` step failed); its `apply` step succeeded. Read the STEP
-- statuses, and then read the database. A job reporting SUCCESS tells you it
-- exited 0, not that your column exists — and a job can correctly report doing
-- exactly what it was asked while what it was asked was not what you needed.
--
-- ⚠⚠ ROLLBACK IS NOT SYMMETRIC. SAID HERE RATHER THAN LEFT FOR AN INCIDENT.
-- ------------------------------------------------------------------------
-- This constraint does NOT tolerate a rollback of consumption below that
-- digest. That is a deliberate choice, not an oversight, and the alternatives
-- were considered and rejected:
--   * Scoping it by row age (e.g. `OR requested_at < <literal>`) would exempt
--     history but NOT new writes, so a rolled-back binary would still violate
--     it on the very next completion. It buys nothing for rollback and hides
--     the real invariant.
--   * `NOT VALID` skips validation of EXISTING rows only; new writes are still
--     checked, so it likewise does not survive a rollback.
-- There is no formulation that both tolerates the old binary and expresses the
-- invariant, because tolerating the old binary IS the absence of the invariant.
-- THEREFORE: rolling consumption back below that digest MUST run
-- 0115_companion_turn_settled_by_required.down.sql FIRST. The ordering is the
-- mitigation; the DOWN is cheap and instant.
--
-- ⚠ THE BACKFILL LIFTS FORCE RLS, AND MUST (the 0110/0114 lesson)
-- ---------------------------------------------------------------
-- companion_turns is FORCE ROW LEVEL SECURITY, and an owner is NOT exempt from
-- FORCE. Running as chora_consumption_migrate with no tenant GUC, every policy
-- evaluates false, so an UPDATE here would match ZERO rows and silently no-op
-- while ADD CONSTRAINT would still validate EVERY row and fail on exactly the
-- rows the backfill never reached. THE WRITE IS FILTERED AND THE CHECK IS NOT.
--
-- This migration does not merely repeat 0114's lift, it PROVES the lift worked:
-- the remaining-violator count is taken WITH FORCE STILL LIFTED (a count taken
-- under FORCE would be a false zero, the same trap wearing a different hat) and
-- the migration RAISES rather than proceeding if any row survives. A backfill
-- that silently reaches nothing cannot pass this.
-- =============================================================================

BEGIN;

SET LOCAL lock_timeout = '30s';

DO $$
DECLARE
  was_forced   boolean;
  n_timeout    bigint;
  n_completion bigint;
  n_remaining  bigint;
  n_terminal   bigint;
BEGIN
  SELECT relforcerowsecurity INTO was_forced
    FROM pg_class WHERE oid = 'public.companion_turns'::regclass;

  IF was_forced THEN
    EXECUTE 'ALTER TABLE companion_turns NO FORCE ROW LEVEL SECURITY';
  END IF;

  -- Any terminal row carrying a NULL settled_by. Backfill from what status
  -- already tells us, exactly as 0114 did for the history before it.
  -- ⚠ The ordering here was the REVERSE of what was assumed: the
  -- settled_by-writing image went live 13:17Z and 0114 only applied at
  -- 16:18:58Z, so for ~3h the running binary wrote a column that did not exist.
  -- companion_turns held ZERO rows throughout (the lane had no traffic), which
  -- is why that window produced no casualties and why these counts are expected
  -- to be 0. They are still measured rather than assumed.
  -- The status vocabulary is CHECK-constrained to
  -- (accepted, completed, failed, rejected, timeout), so these two statements
  -- cover every terminal value: there is no fifth terminal status to miss.
  UPDATE companion_turns
     SET settled_by = 'timeout'
   WHERE status = 'timeout' AND settled_by IS NULL;
  GET DIAGNOSTICS n_timeout = ROW_COUNT;

  UPDATE companion_turns
     SET settled_by = 'completion'
   WHERE status IN ('completed', 'failed', 'rejected') AND settled_by IS NULL;
  GET DIAGNOSTICS n_completion = ROW_COUNT;

  -- Measured with FORCE STILL LIFTED. Under FORCE this count is a false zero.
  SELECT count(*) INTO n_remaining
    FROM companion_turns WHERE status <> 'accepted' AND settled_by IS NULL;
  SELECT count(*) INTO n_terminal
    FROM companion_turns WHERE status <> 'accepted';

  RAISE NOTICE '0115 backfill: timeout=%, completion=%, terminal_rows=%, remaining_violators=%',
    n_timeout, n_completion, n_terminal, n_remaining;

  -- Fail loud rather than hand a doomed ADD CONSTRAINT a table it will reject.
  IF n_remaining > 0 THEN
    RAISE EXCEPTION '0115: % terminal companion_turns rows still carry a NULL settled_by after the backfill; the constraint would reject them', n_remaining;
  END IF;

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
    RAISE EXCEPTION '0115: companion_turns left without FORCE ROW LEVEL SECURITY';
  END IF;
END $$;

-- The constraint 0114 deferred. A turn that is no longer accepted must say what
-- settled it: 'completion' (an answerer spoke, including FAILED and REJECTED
-- results) or 'timeout' (the deadline passed and it was reaped).
ALTER TABLE companion_turns
  ADD CONSTRAINT companion_turns_settled_by_required
  CHECK (status = 'accepted' OR settled_by IS NOT NULL);

COMMENT ON CONSTRAINT companion_turns_settled_by_required ON companion_turns IS
  'AUDIT-G5: a terminal turn must record WHAT settled it. Deferred by 0114 until the image that stamps settled_by was confirmed serving (digest 75333a903944, 2026-08-23T13:17Z). Does NOT tolerate a rollback below that digest: run 0115''s DOWN first.';

COMMIT;

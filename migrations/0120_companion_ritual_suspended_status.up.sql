-- =============================================================================
-- chora-consumption : 0120_companion_ritual_suspended_status.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Track         : UX refactor Phase D, package D2 (N13)
-- ADR           : ADR-257 D7, ADR-252 D5 and D6
-- Date          : 2026-09-02
--
-- Widens the run status CHECK to admit a third terminal reason,
-- 'companion_suspended', and adds the column that names WHICH companion is
-- paused.
--
-- WHY
-- StepExecResult.Blocked carried exactly one meaning, an Armor content block,
-- rendered to the learner as "stopped for safety". ADR-252 introduces a second,
-- different denial: an operator suspends a companion, and the gateway refuses
-- its turns with FAILED_PRECONDITION and reason companion_suspended.
--
-- With only two reasons that denial arrived as a generic step error and the run
-- terminated as 'failed', which reads to a learner as a fault in the product
-- rather than as a deliberate pause somebody applied. A peer step makes it
-- materially worse, because the suspended companion may not be the one the
-- learner is looking at.
--
-- The value matches the kennel's REJECTED error_code
-- (companion.KennelErrCodeCompanionSuspended) rather than inventing a third
-- spelling of the same fact.
--
-- WHY NOT AN ENUM
-- The column is TEXT with a CHECK, as 0071 wrote it. That stays: widening a
-- CHECK is a transactional DDL that takes an ACCESS EXCLUSIVE lock for the
-- validation scan only, while a PG enum's ADD VALUE could not run inside this
-- transaction at all before PG 12 and still cannot be removed by a down
-- migration. A CHECK is the reversible shape and this table is small.
--
-- THE VALIDATION SCAN IS THE POINT
-- The constraint is added WITHOUT NOT VALID deliberately. NOT VALID would skip
-- the scan and leave already-invalid rows in place, which is exactly what a
-- status column must never allow: the whole value of this constraint is that
-- every row in the table is one of the terminals the domain knows how to
-- render. companion_ritual_runs is an append-only audit table of individual
-- runs, so the scan is cheap and the guarantee is worth it.
--
-- ORDER
-- DROP then ADD, not ADD then DROP: two CHECKs on the same column would both
-- have to pass, so the narrow one would keep refusing the new value for as long
-- as it existed. Both statements are in one transaction, so no window exists
-- where the column is unconstrained.
--
-- WHY paused_companion_id IS A SEPARATE COLUMN
-- The run already has an `error` TEXT that carries the human message, and the id
-- could have been parsed back out of it. It is not, because a message is prose
-- that a later edit will reword, and a client that scraped an id out of prose
-- would break silently on that edit. A nullable column says the same fact in a
-- shape nothing has to parse.
--
-- Nullable with NO default, unlike step_outputs in 0119. There, an empty array
-- was the honest reading of "this run produced no step outputs". Here NULL
-- carries a real meaning that no other value can: this run was not paused. A
-- '' default would make every completed run assert an empty pause.
--
-- CLOSURE
-- It is a companion id, not a learner id, and the row is already pseudonymised
-- through owner_gcid, so it needs no closure entry of its own.
--
-- RLS unchanged; no RLS-bypass surface is opened.
-- =============================================================================

BEGIN;

ALTER TABLE companion_ritual_runs
    DROP CONSTRAINT IF EXISTS companion_ritual_runs_status_check;

ALTER TABLE companion_ritual_runs
    ADD CONSTRAINT companion_ritual_runs_status_check
    CHECK (status IN ('running','completed','failed','skipped_budget','blocked','companion_suspended'));

ALTER TABLE companion_ritual_runs
    ADD COLUMN IF NOT EXISTS paused_companion_id UUID NULL;

COMMENT ON COLUMN companion_ritual_runs.paused_companion_id IS
    'N13 (ADR-257 D7): the companion an operator suspended, set only on a companion_suspended terminal. NULL on every other terminal, and NULL means this run was not paused. Under a peer step this may not be the companion the learner is looking at, which is why the run carries it rather than letting the reader assume.';

COMMENT ON COLUMN companion_ritual_runs.status IS
    'Run terminal. companion_suspended (N13, ADR-257 D7) is an ADR-252 containment denial: an operator paused a companion, so it is a designed PAUSE like skipped_budget and never a fault. Distinct from blocked, which means an Armor content block. See paused_companion_id for which companion.';

COMMIT;

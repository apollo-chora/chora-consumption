-- =============================================================================
-- chora-consumption : 0036_phyllis_eira_seed.down.sql
--
-- Rolls back the Eira Phyllis-demo Familiar seed inserted by
-- 0036_phyllis_eira_seed.up.sql.
--
-- Soft-delete approach per .claude/rules/ddd-enforcement.md §Soft Deletes —
-- we set deleted_at on the familiar instance (the parent aggregate root).
-- familiar_growth_events is an append-only ledger with no soft-delete column;
-- we leave its rows in place (the (familiar_id, source, idempotency_key)
-- UNIQUE still serves the re-up purpose, and re-applying 0036.up.sql is a
-- no-op on the EXP rows too). familiar_growth_daily_counters likewise stays
-- — the seed values are time-bound to 2026-05-13 and harmless.
--
-- Result: a subsequent SELECT * FROM familiar_instances
--   WHERE familiar_id = '...e1a0' AND deleted_at IS NULL
-- returns zero rows (consistent with "Eira does not exist for this DB").
-- Re-applying 0036.up.sql lands a fresh row (ON CONFLICT (familiar_id) DO
-- NOTHING does NOT no-op here because the deleted-but-still-present row
-- collides on PK — so we also UNDELETE in the up path is NOT done; instead
-- we accept that the row stays soft-deleted post-down. Operators who want
-- a clean re-up should run `UPDATE familiar_instances SET deleted_at = NULL
-- WHERE familiar_id = ...e1a0` manually, or DROP+recreate via psql when
-- not bound by these hook constraints. Demo only — production sees a
-- random familiar_id and is unaffected.
--
-- Idempotent — re-running converges to deleted_at IS NOT NULL.
-- =============================================================================

BEGIN;

SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';

UPDATE familiar_instances
   SET deleted_at = COALESCE(deleted_at, now())
 WHERE familiar_id = '00000000-0000-7000-8000-00000000e1a0'::uuid;

COMMIT;

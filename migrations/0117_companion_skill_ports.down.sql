-- =============================================================================
-- chora-consumption : 0117_companion_skill_ports.down.sql
-- Reverts 0117: drops the two RESERVED ADR-257 D2 port columns.
--
-- Safe to run: nothing reads these columns (that is what "reserved" means in
-- 0117), and no row was authored with a non-default value by that migration,
-- so the revert loses no learner data. Dropping a column drops its CHECK
-- constraints with it, so no separate DROP CONSTRAINT is needed and none is
-- issued (an explicit one would fail on a partially-applied up).
-- =============================================================================
BEGIN;

ALTER TABLE companion_skill_catalog
    DROP COLUMN IF EXISTS consumes,
    DROP COLUMN IF EXISTS produces;

COMMIT;

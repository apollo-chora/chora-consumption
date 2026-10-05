-- =============================================================================
-- chora-consumption : 0112_companion_suspension_projection.down.sql
--
-- Inverse of 0112_companion_suspension_projection.up.sql: drops the advisory
-- projection table (index + RLS policy drop with it). Safe to roll back at
-- any time: the table is a rebuildable read-copy of the governance audit
-- stream, and the model gateway (the control) never reads it.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS companion_suspension_projection;

COMMIT;

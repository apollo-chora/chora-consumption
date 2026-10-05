-- =============================================================================
-- chora-consumption : 0067_ceremony_edge_scout_runs.down.sql
-- Reverses 0067_ceremony_edge_scout_runs.up.sql (CHO-2040 R8-1 first-run
-- ledger). Dropping the table re-grants every learner's ceremony freebie —
-- only roll back together with the Unit B runner build.
-- =============================================================================
BEGIN;

DROP TABLE IF EXISTS ceremony_edge_scout_runs;

COMMIT;

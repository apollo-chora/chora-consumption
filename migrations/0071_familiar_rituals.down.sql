-- =============================================================================
-- chora-consumption : 0071_familiar_rituals.down.sql
-- Reverses 0071_familiar_rituals.up.sql (CHO-2016 Grimoire Rituals v1).
-- Dropping these tables discards every learner-designed Ritual + its
-- append-only revisions + stamped run history — roll back only together with
-- the Ritual runner build (G2) and the HTTP/route wiring (G5).
-- No FKs between the three, so drop order is free; done children-first for tidiness.
-- =============================================================================
BEGIN;

DROP TABLE IF EXISTS familiar_ritual_runs;
DROP TABLE IF EXISTS familiar_ritual_revisions;
DROP TABLE IF EXISTS familiar_rituals;

COMMIT;

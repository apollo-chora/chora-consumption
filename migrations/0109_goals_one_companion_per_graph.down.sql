-- chora-consumption : 0109_goals_one_companion_per_graph.down.sql
-- Reverses 0109: drops the one-Companion-per-graph uniqueness guarantee.
--
-- NB the 409 in goals_handler.go survives this and keeps refusing the common
-- case, so dropping the index reopens the RACE (two concurrent attaches both
-- passing the read-then-write check), not the whole hole.

BEGIN;

DROP INDEX IF EXISTS ux_goals_attached_familiar_live;

COMMIT;

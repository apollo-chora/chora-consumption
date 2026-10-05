-- =============================================================================
-- chora-consumption : 0104_concept_sub_goal.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-2328: ADR-247 D1 per-node sub-goal
-- Date          : 2026-07-22
--
-- Purpose:
--   Give each learner-owned ConceptNode a stored sub_goal (the learner's
--   per-node objective, "mastery here means ...") plus its provenance. The
--   sub_goal drives ADR-247 Capability A (weakness-aware hex question
--   generation) and Capability B (weakness-aware fog suggestions) so both
--   KG-generation surfaces target what the learner is trying to achieve on the
--   concept, instead of a title-only prompt. It also fixes "I do not see the
--   sub-goals": the drawer now has a stored objective to render and edit.
--
-- -----------------------------------------------------------------------------
-- NULLABLE, NO CHECK, NO DEFAULT
-- -----------------------------------------------------------------------------
-- A concept legitimately has no sub-goal (the default state), so the column is
-- nullable with no backfill. sub_goal_provenance carries the consumption-owned
-- provenance vocabulary (learner_authored primary; familiar_suggested_accepted
-- and system_derived are follow-on lanes), but it is left UNCHECKED: the domain
-- aggregate (ConceptNode.SetSubGoal) validates provenance and writes an empty
-- string on clear, so a DB CHECK would only add a way to reject our own cleared
-- state. Reads COALESCE both columns to '' so a NULL and a cleared '' are one
-- "unset" sentinel in the domain.
--
-- NO GRANT NEEDED: 9999_grant_app_roles.sql grants at TABLE level, which covers
-- columns added later; this is an ALTER on the already-granted 0056 table, so a
-- targeted apply does not hit the 9999-skipping 42501 trap.
--
-- ROW LEVEL SECURITY: unchanged. The 0056 tenant/learner policy still applies;
-- adding a column changes no policy and needs none.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS).
-- =============================================================================

BEGIN;

ALTER TABLE concept_nodes
    ADD COLUMN IF NOT EXISTS sub_goal TEXT,
    ADD COLUMN IF NOT EXISTS sub_goal_provenance TEXT;

COMMENT ON COLUMN concept_nodes.sub_goal IS
    'The learner''s per-node objective ("mastery here means ..."), or NULL when '
    'unset. Drives ADR-247 Capability A (hex question generation) and Capability '
    'B (fog suggestions). ADR-247 D1, CHO-2328.';

COMMENT ON COLUMN concept_nodes.sub_goal_provenance IS
    'WHO authored the sub_goal: learner_authored (primary), '
    'familiar_suggested_accepted or system_derived (follow-on lanes), or NULL '
    'when unset. Domain-validated (ConceptNode.SetSubGoal); unchecked here so a '
    'cleared empty-string state is never rejected. ADR-247 D1, CHO-2328.';

COMMIT;

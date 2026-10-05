-- =============================================================================
-- chora-consumption : 0098_diagnose_goal_scope.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-238 — marked-test Diagnose is goal-scoped, 2026-07-16
-- Architecture  : docs/architecture/adrs/adr-238-marked-test-diagnose-goal-scope.md
--
-- Purpose (ADR-238 Implementation decision — resolve in chora-consumption):
--   A marked test spans a whole goal (a map of concepts), so a detected weakness
--   must land on the BEST-MATCHING on-map concept, not the entry node. Two
--   additive, nullable columns carry the goal scope + the resolved target:
--
--   1. weakness_doc_uploads.goal_id — the goal (map) the upload was scoped to.
--      The ingest resolver (weakness_analyzed_subscriber) reads it to fetch that
--      goal's concept subtree and nearest-match each edge against it. NULL = a
--      non-goal-scoped upload (legacy / derived) → today's slug-only behaviour.
--
--   2. learner_weakness.target_concept_id — the on-map ConceptNode the edge was
--      resolved to (pgvector nearest-match at ingest). Lets the read-side overlay
--      key on a STABLE concept id instead of the fragile concept_key slug that
--      the analyser invented. NULL = UNMATCHED (surfaced at goal level + a D4
--      concept suggestion) OR a pre-0098 row (unchanged, slug-join still applies).
--
-- Cross-aggregate refs (goal_id, target_concept_id) are UUID WITHOUT FK per
-- .claude/rules/ddd-enforcement.md §3 (concept_nodes / goals are sibling
-- aggregates in the same DB; app-validated, no FK).
--
-- RLS is already ENABLE + FORCE on both tables (0046 / 0047); a new column needs
-- no policy change. Grants handled by 9999_grant_app_roles.sql (lex-last).
-- =============================================================================

BEGIN;

-- 1. The goal (map) an upload was scoped to. Drives the ingest resolver's
--    candidate concept set. NULL ⇒ non-goal upload (unchanged behaviour).
ALTER TABLE weakness_doc_uploads
    ADD COLUMN IF NOT EXISTS goal_id UUID;

COMMENT ON COLUMN weakness_doc_uploads.goal_id IS
    'ADR-238: the goal (map) this upload was scoped to; the ingest resolver '
    'nearest-matches each edge against this goal''s concept subtree. NULL = '
    'non-goal-scoped upload. UUID, no FK (ddd-enforcement §3).';

-- 2. The on-map ConceptNode a Growth Edge resolved to at ingest (pgvector
--    nearest-match). The read-side overlay prefers this stable id over the
--    concept_key slug. NULL ⇒ UNMATCHED / pre-0098 row.
ALTER TABLE learner_weakness
    ADD COLUMN IF NOT EXISTS target_concept_id UUID;

COMMENT ON COLUMN learner_weakness.target_concept_id IS
    'ADR-238: the on-map ConceptNode this edge resolved to (goal-scoped '
    'pgvector nearest-match at ingest). The read overlay id-joins on this; '
    'NULL = UNMATCHED (goal-level + D4 suggestion) or a pre-0098 row. UUID, no FK.';

-- Read path: the Growth-lens / DIAGNOSIS overlay and the goal view fan out the
-- learner's live edges and id-join by target_concept_id. Partial index keeps the
-- resolved-edge lookups cheap without touching the (majority) slug-only rows.
CREATE INDEX IF NOT EXISTS idx_learner_weakness_target_concept
    ON learner_weakness (tenant_id, learner_gcid, target_concept_id)
    WHERE deleted_at IS NULL AND target_concept_id IS NOT NULL;

COMMIT;

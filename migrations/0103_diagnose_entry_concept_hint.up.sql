-- =============================================================================
-- chora-consumption : 0103_diagnose_entry_concept_hint.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-238 D2 - the concept drawer is a soft entry, 2026-07-20
-- Architecture  : docs/architecture/adrs/adr-238-marked-test-diagnose-goal-scope.md
--
-- Purpose (ADR-238 D2 - "the concept drawer is a soft entry, not a filter"):
--   Entering Diagnose from a concept passes that concept as an EMPHASIS HINT,
--   never a hard filter that discards off-concept weaknesses. 0098 landed the
--   goal scope (which BOUNDS the candidate concepts); this column carries the
--   entry concept (which only BIASES the ranking inside that boundary).
--
--   The A+ concept drawer has been sending `concept_id` on the upload since
--   ADR-238 shipped, but no server-side code read it - the hint was accepted and
--   silently discarded. This column is where it lands so the ingest resolver can
--   mark in-subtree candidates as a tie-break.
--
--   NULL is the normal, meaningful case: a GOAL-level "Diagnose my map" upload
--   deliberately carries NO entry concept, so a whole-map diagnosis is never
--   nudged toward an arbitrary node. NULL also covers every pre-0103 row.
--
-- Deliberately NOT a filter, at any layer: the matcher applies the bias only
-- AFTER the cosine floor, so this value can change WHICH concept an edge lands
-- on but never WHETHER it matched. See concept_match.go stage 3.
--
-- Cross-aggregate ref (entry_concept_id -> concept_nodes) is UUID WITHOUT FK per
-- .claude/rules/ddd-enforcement.md section 3 (sibling aggregates in the same DB;
-- app-validated, no FK). No index: the column is read only via the upload's own
-- primary-key row on the ingest path, never queried across.
--
-- RLS is already ENABLE + FORCE on weakness_doc_uploads (0046); a new column
-- needs no policy change. Grants handled by 9999_grant_app_roles.sql (lex-last).
-- =============================================================================

BEGIN;

ALTER TABLE weakness_doc_uploads
    ADD COLUMN IF NOT EXISTS entry_concept_id UUID;

COMMENT ON COLUMN weakness_doc_uploads.entry_concept_id IS
    'ADR-238 D2: the concept whose drawer this Diagnose upload was started from. '
    'A SOFT emphasis hint only - the ingest resolver marks candidates in this '
    'concept''s sub-tree as a tie-break within the goal''s candidate set, applied '
    'AFTER the cosine floor so it can never discard a weakness. NULL = a '
    'goal-level (whole-map) upload, which carries no hint by design, or a '
    'pre-0103 row. UUID, no FK (ddd-enforcement section 3).';

COMMIT;

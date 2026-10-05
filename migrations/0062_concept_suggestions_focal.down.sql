-- =============================================================================
-- chora-consumption : 0062_concept_suggestions_focal.down.sql
-- Reverts 0062: drop the concept_suggestions.focal_concept_id scoping anchor.
-- The list path then falls back to the unfiltered whole-map inbox (ListPending).
-- =============================================================================
BEGIN;

ALTER TABLE concept_suggestions
    DROP COLUMN IF EXISTS focal_concept_id;

COMMIT;

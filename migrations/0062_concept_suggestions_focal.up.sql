-- =============================================================================
-- chora-consumption : 0062_concept_suggestions_focal.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-212 WS-4 (D4/D9) — scope the Familiar SUGGESTION inbox to its focal concept.
--
-- The concept-shaped fog generate is focal-anchored: the requested + emitted
-- events already carry focal_concept_id (the concept the learner asked the
-- Familiar to explore around). Without persisting it, ListPending returned EVERY
-- pending suggestion for the learner regardless of focal, so a stale prior-
-- generate batch (a DIFFERENT focal) leaked onto every node. This adds the
-- scoping anchor so the per-node curation inbox filters to
--   focal_concept_id = <focal> OR focal_concept_id IS NULL (whole-map).
--
-- Nullable, NO backfill: pre-existing rows stay NULL = a whole-map suggestion
-- (visible on every node), preserving the pre-focal generate behaviour. Opaque
-- concept reference, app-validated (no FK, #3, cross-aggregate). Inherits
-- concept_suggestions' RLS (0060: ENABLE + FORCE + tenant_isolation FOR ALL) — an
-- added column needs no policy change; grants are schema-wide
-- (9999_grant_app_roles.sql). Idempotent (ADD COLUMN IF NOT EXISTS).
-- =============================================================================
BEGIN;

ALTER TABLE concept_suggestions
    ADD COLUMN IF NOT EXISTS focal_concept_id UUID;   -- the focal concept the fog generated around; NULL = whole-map (#3, no FK)

COMMENT ON COLUMN concept_suggestions.focal_concept_id IS
    'ADR-212 WS-4: the focal concept this suggestion was generated around (opaque UUID, no FK). NULL = whole-map suggestion, visible on every node.';

COMMIT;

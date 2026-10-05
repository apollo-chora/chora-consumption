-- =============================================================================
-- chora-consumption : 0057_goal_root_concept_and_personal_completion.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-214 WS-1 (goal = evolving root concept) + ADR-213 WS-1 (bifurcated mastery,
-- personal-axis completion). Amends goals (migrations/0051_goals.up.sql).
--   root_concept_id       — the Goal's evolving root ConceptNode anchor (ADR-214
--                           §1). Opaque cross-aggregate ref, NO FK (#3), nullable.
--   personal_completed_at — the learner-DEFINED personal-axis completion (ADR-213
--                           §1), orthogonal to the verified Status/Graduate axis.
-- Additive; soft-delete unchanged; RLS unchanged (inherited from 0051).
-- =============================================================================
BEGIN;

ALTER TABLE goals
    ADD COLUMN IF NOT EXISTS root_concept_id       UUID,        -- ADR-214 §1 anchor; no FK (#3)
    ADD COLUMN IF NOT EXISTS personal_completed_at TIMESTAMPTZ; -- ADR-213 §1 personal-axis completion

-- ADR-214 §3: the self-completing CREDENTIAL kinds (cert/course/path) are REMOVED
-- — they were the credential-forgery hole (a personal goal graduating from
-- personal KG-mastery under a credential lens). Any existing such row becomes a
-- personal `curiosity` goal; a real relationship to an operator credential is
-- re-established via the ADR-216 aspiration link, never a self-completing kind.
-- (Deliberate, documented enum-value removal — NOT a silent runtime fallback.)
--
-- goals is FORCE ROW LEVEL SECURITY and the migrate role owns the table but has
-- no BYPASSRLS, so a plain UPDATE here is RLS-filtered to 0 rows (no tenant
-- context) — the normalisation silently no-ops and the ADD CONSTRAINT below then
-- fails validation against the un-normalised physical heap ("check constraint
-- goals_kind_check is violated by some row"). Temporarily drop FORCE so the
-- owner-run normalisation reaches every tenant's rows, then restore it. The
-- table's tenant_isolation policy is unchanged; only the owner-FORCE flag toggles
-- for the span of this one owner-run UPDATE.
ALTER TABLE goals NO FORCE ROW LEVEL SECURITY;
UPDATE goals SET kind = 'curiosity'
 WHERE kind IN ('cert', 'course', 'path');
ALTER TABLE goals FORCE ROW LEVEL SECURITY;

ALTER TABLE goals DROP CONSTRAINT IF EXISTS goals_kind_check;
ALTER TABLE goals
    ADD CONSTRAINT goals_kind_check
    CHECK (kind IN ('curiosity', 'theme_mastery', 'edge'));

-- Grants handled by 9999_grant_app_roles.sql (no explicit GRANT here).
COMMIT;

-- =============================================================================
-- chora-consumption : 0061_familiar_capability_catalogue_v2.down.sql
--
-- Reverts the ADR-218 catalogue v2 rebase (CHO-2012). The familiar_instances
-- slots/tier re-base is NOT reverted (the pre-P0 values derived from a
-- retired per-user-XP axis and cannot be reconstructed; stage-derived values
-- are correct under both regimes).
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS species_paths;

ALTER TABLE familiar_skill_grants
    DROP CONSTRAINT IF EXISTS chk_skill_grants_unlocked_via;
DROP INDEX IF EXISTS idx_familiar_skill_grants_equipped;
ALTER TABLE familiar_skill_grants
    DROP COLUMN IF EXISTS equipped,
    DROP COLUMN IF EXISTS unlocked_via,
    DROP COLUMN IF EXISTS unlocked_at_stage;

-- Remove the 27 seeded rows (safe: pre-0061 the catalogue was empty; grants
-- FK-RESTRICT would block if any grant referenced them, which is the correct
-- fail-loud behaviour for a down applied after real usage).
DELETE FROM familiar_skill_catalog WHERE skill_key IN (
    'explain_anew','quiz_me','worked_example','socratic_drill','flashcard_forge',
    'step_checker','polyglot','map_sight','weakness_sight','progress_mirror',
    'recap_scribe','photo_sight','reminder_bell','path_weaver','fog_scout',
    'goal_scribe','atom_forge','study_calendar','web_research','source_reader',
    'fact_check','duel_second','dawn_briefing','watchful_eye',
    'long_weaving','twin_rituals','weave_mastery'
);

ALTER TABLE familiar_skill_catalog
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_kind,
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_min_stage,
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_slot_cost,
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_family,
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_policy_class,
    DROP CONSTRAINT IF EXISTS chk_skill_catalog_output_sink;
ALTER TABLE familiar_skill_catalog
    DROP COLUMN IF EXISTS skill_kind,
    DROP COLUMN IF EXISTS min_growth_stage,
    DROP COLUMN IF EXISTS slot_cost,
    DROP COLUMN IF EXISTS family,
    DROP COLUMN IF EXISTS policy_class,
    DROP COLUMN IF EXISTS output_sink,
    DROP COLUMN IF EXISTS price_key,
    DROP COLUMN IF EXISTS prompt_fragment_ref,
    DROP COLUMN IF EXISTS params_schema,
    DROP COLUMN IF EXISTS eval_ref,
    DROP COLUMN IF EXISTS active;

COMMENT ON COLUMN familiar_skill_catalog.xp_unlock_threshold IS NULL;
COMMENT ON TABLE familiar_progression_tiers IS NULL;

COMMIT;

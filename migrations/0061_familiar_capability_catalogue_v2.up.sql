-- =============================================================================
-- chora-consumption : 0061_familiar_capability_catalogue_v2.up.sql
--
-- ADR           : ADR-218 — Familiar capability catalogue + slot choice
--                 (species Path); amendment 2026-07-02 skill_kind {active,craft}
-- CR            : docs/FAMILIAR-GROWTH-AGENT-BUILDER-CR-2026-07-02.md (§5.7)
-- Spec (seed source of truth): docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §5 columns
--                 + §2/§3 sheets + §4 Paths. Go mirror + SQL generator:
--                 internal/domain/familiar/seedspec (the migration-content test
--                 regenerates the marker-delimited seed blocks below and fails
--                 on ANY drift — hand-edits to those blocks are rejected).
-- Story         : CHO-2012 (P0, dark: every catalogue row seeds active=FALSE;
--                 the ADR-174 eval gate flips the Launch 7 in P2)
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Date          : 2026-07-03
--
-- What this does:
--   1. familiar_skill_catalog v2 — ADD skill_kind / min_growth_stage /
--      slot_cost / family / policy_class / output_sink / price_key /
--      prompt_fragment_ref / params_schema / eval_ref / active.
--      xp_unlock_threshold (per-USER XP, the phantom axis) goes DEAD: the
--      read path is removed in code; the column stays for a later contract
--      cycle (ADR-218 C2/D8). prompt_fragment_ref + eval_ref stay NULL in
--      P0 — no fabricated pointers; P2 fills them when the ADR-197
--      fragments + ADR-174 golden suites exist.
--   2. species_paths (NEW, platform-scoped, NO RLS) — per-species ordered
--      unlock sequence over the full catalogue; one ACTIVE version per
--      species; fail-loud floor validation lives app-side
--      (familiar.ValidatePath) + in the CI seed test (seedspec).
--   3. familiar_skill_grants — ADD equipped / unlocked_via /
--      unlocked_at_stage. A grant row = OWNED FOREVER; the slot cap binds
--      only the equipped set (ADR-218 D3).
--   4. familiar_instances data re-base — skill_slots_unlocked = stage + 1
--      (clamped 7) + evolution_tier = the L6 derived band (ADR-218 D2/D8).
--      The award tx maintains both from now on. RLS NOTE: familiar_instances
--      is FORCE RLS — the UPDATE toggles NO FORCE around itself so the
--      owner-run migration actually updates rows (WS-4 lesson: an owner-run
--      UPDATE on a FORCE-RLS table silently no-ops).
--   5. Seed the 27-row catalogue (24 active + 3 craft) + the 5 hero Paths.
--
-- HARD INVARIANT: idempotent + revertable; every statement guarded.
-- GRANTs delegated to 9999_grant_app_roles.sql.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1. familiar_skill_catalog v2 columns
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_skill_catalog
    ADD COLUMN IF NOT EXISTS skill_kind TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS min_growth_stage SMALLINT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS slot_cost SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS family TEXT,
    ADD COLUMN IF NOT EXISTS policy_class TEXT NOT NULL DEFAULT 'standard',
    ADD COLUMN IF NOT EXISTS output_sink TEXT,
    ADD COLUMN IF NOT EXISTS price_key TEXT,
    ADD COLUMN IF NOT EXISTS prompt_fragment_ref TEXT,
    ADD COLUMN IF NOT EXISTS params_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS eval_ref TEXT,
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT FALSE;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_kind
        CHECK (skill_kind IN ('active','craft'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_min_stage
        CHECK (min_growth_stage BETWEEN 0 AND 6);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_slot_cost
        CHECK (slot_cost BETWEEN 0 AND 2);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_family
        CHECK (family IS NULL OR family IN
               ('scholar','sight','weaver','seeker','companion','habit','craft'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_policy_class
        CHECK (policy_class IN ('standard','generative','external_egress','autonomy'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE familiar_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_output_sink
        CHECK (output_sink IS NULL OR output_sink IN
               ('chat','memory_note','suggestion_inbox','question_bank',
                'notification','calendar_artifact'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

COMMENT ON COLUMN familiar_skill_catalog.xp_unlock_threshold IS
    'DEAD since CHO-2012 (ADR-218 C2/D8): the per-user XP unlock axis is retired; '
    'unlocks ride the species Path (min_growth_stage floors). Column retained for '
    'a later contract cycle — no code reads it.';

-- -----------------------------------------------------------------------------
-- 2. species_paths — platform reference data (NO RLS, like the catalogue)
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS species_paths (
    path_id     UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    species     TEXT         NOT NULL,
    version     INT          NOT NULL DEFAULT 1 CHECK (version >= 1),
    entries     JSONB        NOT NULL,
    active      BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CHECK (species IN ('owl','fox','dragon','phoenix','penguin'))
);

-- Exactly one ACTIVE path per species.
CREATE UNIQUE INDEX IF NOT EXISTS uq_species_paths_active
    ON species_paths (species) WHERE active;

-- -----------------------------------------------------------------------------
-- 3. familiar_skill_grants — loadout columns (ADR-218 D3)
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_skill_grants
    ADD COLUMN IF NOT EXISTS equipped BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS unlocked_via TEXT,
    ADD COLUMN IF NOT EXISTS unlocked_at_stage SMALLINT;

DO $$ BEGIN
    ALTER TABLE familiar_skill_grants
        ADD CONSTRAINT chk_skill_grants_unlocked_via
        CHECK (unlocked_via IS NULL OR unlocked_via IN
               ('species_path','admin_grant','aha_preview'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS idx_familiar_skill_grants_equipped
    ON familiar_skill_grants (familiar_id)
    WHERE equipped AND deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- 4. familiar_instances re-base: slots = stage + 1 (≤7), tier = derived band
--    (FORCE-RLS toggle so the owner-run UPDATE is not silently filtered)
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY;

UPDATE familiar_instances
   SET skill_slots_unlocked = LEAST(growth_stage + 1, 7),
       evolution_tier = CASE
                          WHEN growth_stage >= 6 THEN 'sage'
                          WHEN growth_stage >= 4 THEN 'master'
                          WHEN growth_stage >= 2 THEN 'adept'
                          ELSE 'apprentice'
                        END,
       updated_at = now()
 WHERE skill_slots_unlocked IS DISTINCT FROM LEAST(growth_stage + 1, 7)
    OR evolution_tier IS DISTINCT FROM CASE
                          WHEN growth_stage >= 6 THEN 'sage'
                          WHEN growth_stage >= 4 THEN 'master'
                          WHEN growth_stage >= 2 THEN 'adept'
                          ELSE 'apprentice'
                        END;

ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;

COMMENT ON TABLE familiar_progression_tiers IS
    'DORMANT since CHO-2012 (ADR-218 D8): the per-user-XP tier curve is retired; '
    'slots derive from growth_stage (+1/stage) and evolution_tier is the L6 '
    'derived band. Table retained as historical reference — no code reads it.';

-- -----------------------------------------------------------------------------
-- 5. Seeds (generated from internal/domain/familiar/seedspec — DO NOT hand-edit)
-- -----------------------------------------------------------------------------
-- BEGIN GENERATED CATALOGUE SEED (seedspec.CatalogueSeedSQL — do not hand-edit)
INSERT INTO familiar_skill_catalog
    (skill_key, name, description, tool_handler_ref, skill_kind,
     min_growth_stage, slot_cost, family, policy_class, output_sink,
     price_key, params_schema, active)
VALUES
    ('explain_anew', 'Explain It Differently', 'Re-explains a concept through analogies and styles tuned to your interests',
     '["atom.search","atom.cite"]', 'active', 2, 1, 'scholar', 'standard', 'chat', 'familiar_skill_explain_anew', '{}'::jsonb, FALSE),
    ('quiz_me', 'Quick Quiz', '3-5 question micro-quiz pointed at a concept, due items, or weak spots',
     '["bank.query","ebbinghaus_state","qgen.invoke"]', 'active', 3, 1, 'scholar', 'standard', 'chat', 'familiar_skill_quiz_me', '{}'::jsonb, FALSE),
    ('worked_example', 'Worked Example', 'Step-by-step walkthrough of a problem type from your theme',
     '["atom.search","atom.cite"]', 'active', 3, 1, 'scholar', 'standard', 'chat', 'familiar_skill_worked_example', '{}'::jsonb, FALSE),
    ('socratic_drill', 'Socratic Drill', 'Question-first drilling from existing bank questions; never reveals early',
     '["bank.query"]', 'active', 4, 1, 'scholar', 'standard', 'chat', 'familiar_skill_socratic_drill', '{}'::jsonb, FALSE),
    ('flashcard_forge', 'Flashcard Forge', 'Generates flashcards from a concept into your own bank',
     '["qgen.invoke"]', 'active', 4, 1, 'scholar', 'generative', 'question_bank', 'familiar_skill_flashcard_forge', '{}'::jsonb, FALSE),
    ('step_checker', 'Step Checker', 'Verifies your worked steps and points at the FIRST wrong step',
     '["atom.cite"]', 'active', 3, 1, 'scholar', 'standard', 'chat', 'familiar_skill_step_checker', '{}'::jsonb, FALSE),
    ('polyglot', 'Polyglot', 'Re-teaches a concept bilingually in a language you choose',
     '["atom.search","atom.cite"]', 'active', 3, 1, 'scholar', 'standard', 'chat', 'familiar_skill_polyglot', '{}'::jsonb, FALSE),
    ('map_sight', 'Map Sight', 'Answers grounded in YOUR map, resonance-aware, honest about gaps',
     '["kg.read_map"]', 'active', 3, 1, 'sight', 'standard', 'chat', 'familiar_skill_map_sight', '{}'::jsonb, FALSE),
    ('weakness_sight', 'Weakness Sight', 'Explains WHY you are weak somewhere plus concrete next steps',
     '["weakness.read"]', 'active', 3, 1, 'sight', 'standard', 'chat', 'familiar_skill_weakness_sight', '{}'::jsonb, FALSE),
    ('progress_mirror', 'Progress Mirror', 'Narrates your verified profile: courses, scores, certs, trends',
     '["profile.read"]', 'active', 1, 1, 'sight', 'standard', 'chat', 'familiar_skill_progress_mirror', '{}'::jsonb, FALSE),
    ('recap_scribe', 'Recap Scribe', 'Turns a study session into a short memory note you can see and steer',
     '[]', 'active', 2, 1, 'sight', 'standard', 'memory_note', 'familiar_skill_recap_scribe', '{}'::jsonb, FALSE),
    ('photo_sight', 'Photo Sight', 'Reads a snapped page of homework or notes into weakness signals and a note',
     '["doc.ingest"]', 'active', 4, 1, 'sight', 'generative', 'memory_note', 'familiar_skill_photo_sight', '{}'::jsonb, FALSE),
    ('reminder_bell', 'Reminder Bell', 'Schedules due-review nudges at Ebbinghaus-optimal times',
     '["notify.schedule"]', 'active', 2, 1, 'weaver', 'standard', 'notification', 'familiar_skill_reminder_bell', '{}'::jsonb, FALSE),
    ('path_weaver', 'Path Weaver', 'Composes a personalised practice plan from weak, due, and goal',
     '["path.draft","weakness.read","ebbinghaus_state"]', 'active', 4, 2, 'weaver', 'standard', 'chat', 'familiar_skill_path_weaver', '{}'::jsonb, FALSE),
    ('fog_scout', 'Fog Scout', 'Scouts new concepts and edges toward a direction you point',
     '["kg.suggest"]', 'active', 4, 1, 'weaver', 'generative', 'suggestion_inbox', 'familiar_skill_fog_scout', '{}'::jsonb, FALSE),
    ('goal_scribe', 'Goal Scribe', 'Proposes next-goal candidates at graduation',
     '["profile.read","kg.read_map"]', 'active', 4, 1, 'weaver', 'standard', 'chat', 'familiar_skill_goal_scribe', '{}'::jsonb, FALSE),
    ('atom_forge', 'Atom Forge', 'Authors fresh practice atoms on demand into your own bank',
     '["qgen.invoke"]', 'active', 6, 2, 'weaver', 'generative', 'question_bank', 'familiar_skill_atom_forge', '{}'::jsonb, FALSE),
    ('study_calendar', 'Study Calendar', 'Exports your review schedule as calendar entries',
     '["ebbinghaus_state"]', 'active', 4, 1, 'weaver', 'standard', 'calendar_artifact', 'familiar_skill_study_calendar', '{}'::jsonb, FALSE),
    ('web_research', 'Far Sight', 'Grounded web search in a direction you point; returns a cited note',
     '["gateway.grounded_search","kg.suggest"]', 'active', 5, 2, 'seeker', 'external_egress', 'memory_note', 'familiar_skill_web_research', '{}'::jsonb, FALSE),
    ('source_reader', 'Source Reader', 'Reads a URL or document you give it into theme knowledge, with citations',
     '["doc.ingest"]', 'active', 4, 1, 'seeker', 'external_egress', 'memory_note', 'familiar_skill_source_reader', '{}'::jsonb, FALSE),
    ('fact_check', 'Fact Check', 'Verifies a claim with cited sources',
     '["gateway.grounded_search"]', 'active', 5, 1, 'seeker', 'external_egress', 'chat', 'familiar_skill_fact_check', '{}'::jsonb, FALSE),
    ('duel_second', 'Duel Second', 'Pre- and post-duel coaching off your duel history',
     '["duel.read"]', 'active', 4, 1, 'companion', 'standard', 'chat', 'familiar_skill_duel_second', '{}'::jsonb, FALSE),
    ('dawn_briefing', 'Dawn Briefing', 'Prepares a morning digest before you arrive (consented, budgeted)',
     '["ebbinghaus_state","profile.read","kg.suggest"]', 'active', 5, 2, 'habit', 'autonomy', 'notification', 'familiar_skill_dawn_briefing', '{}'::jsonb, FALSE),
    ('watchful_eye', 'Watchful Eye', 'Watches for new atoms in your theme and refreshes suggestions',
     '["kg.suggest"]', 'active', 5, 2, 'habit', 'autonomy', 'suggestion_inbox', 'familiar_skill_watchful_eye', '{}'::jsonb, FALSE),
    ('long_weaving', 'Long Weaving', 'Craft: raises the Ritual step cap from 5 to 8 for this familiar',
     '[]', 'craft', 5, 0, 'craft', 'standard', NULL, NULL, '{}'::jsonb, FALSE),
    ('twin_rituals', 'Twin Rituals', 'Craft: raises the enabled-Rituals quota from 2 to 4',
     '[]', 'craft', 5, 0, 'craft', 'standard', NULL, NULL, '{}'::jsonb, FALSE),
    ('weave_mastery', 'Weave Mastery', 'Craft: unlocks the interactive node canvas and branching Ritual grammar',
     '[]', 'craft', 6, 0, 'craft', 'standard', NULL, NULL, '{}'::jsonb, FALSE)
ON CONFLICT (skill_key) DO NOTHING;
-- END GENERATED CATALOGUE SEED

-- BEGIN GENERATED SPECIES PATHS SEED (seedspec.SpeciesPathsSeedSQL — do not hand-edit)
INSERT INTO species_paths (species, version, entries, active)
VALUES
    ('dragon', 1, '["progress_mirror","explain_anew","reminder_bell","recap_scribe","quiz_me","step_checker","weakness_sight","worked_example","socratic_drill","path_weaver","goal_scribe","flashcard_forge","duel_second","map_sight","web_research","fact_check","dawn_briefing","twin_rituals","long_weaving","photo_sight","fog_scout","watchful_eye","source_reader","study_calendar","polyglot","atom_forge","weave_mastery"]'::jsonb, TRUE),
    ('fox', 1, '["progress_mirror","explain_anew","recap_scribe","reminder_bell","map_sight","quiz_me","polyglot","worked_example","fog_scout","goal_scribe","weakness_sight","photo_sight","socratic_drill","study_calendar","web_research","fact_check","source_reader","twin_rituals","long_weaving","path_weaver","watchful_eye","dawn_briefing","flashcard_forge","step_checker","duel_second","atom_forge","weave_mastery"]'::jsonb, TRUE),
    ('owl', 1, '["progress_mirror","explain_anew","recap_scribe","reminder_bell","worked_example","map_sight","quiz_me","weakness_sight","step_checker","socratic_drill","source_reader","flashcard_forge","path_weaver","polyglot","fact_check","web_research","long_weaving","photo_sight","study_calendar","goal_scribe","twin_rituals","watchful_eye","dawn_briefing","fog_scout","duel_second","atom_forge","weave_mastery"]'::jsonb, TRUE),
    ('penguin', 1, '["progress_mirror","reminder_bell","recap_scribe","explain_anew","quiz_me","worked_example","map_sight","polyglot","study_calendar","duel_second","weakness_sight","socratic_drill","flashcard_forge","goal_scribe","dawn_briefing","watchful_eye","twin_rituals","long_weaving","path_weaver","step_checker","source_reader","web_research","fact_check","photo_sight","fog_scout","atom_forge","weave_mastery"]'::jsonb, TRUE),
    ('phoenix', 1, '["progress_mirror","recap_scribe","reminder_bell","explain_anew","weakness_sight","worked_example","quiz_me","step_checker","photo_sight","socratic_drill","path_weaver","flashcard_forge","map_sight","goal_scribe","dawn_briefing","watchful_eye","long_weaving","twin_rituals","web_research","study_calendar","source_reader","fact_check","fog_scout","duel_second","polyglot","atom_forge","weave_mastery"]'::jsonb, TRUE)
ON CONFLICT (species) WHERE active DO NOTHING;
-- END GENERATED SPECIES PATHS SEED

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--   SELECT count(*) FROM familiar_skill_catalog WHERE deleted_at IS NULL; -- 27
--   SELECT count(*) FROM familiar_skill_catalog WHERE active;             -- 0 (dark until P2)
--   SELECT species, jsonb_array_length(entries) FROM species_paths
--    WHERE active ORDER BY species;                                       -- 5 rows × 27
--   SELECT count(*) FROM familiar_instances
--    WHERE skill_slots_unlocked <> LEAST(growth_stage + 1, 7);            -- 0
-- =============================================================================

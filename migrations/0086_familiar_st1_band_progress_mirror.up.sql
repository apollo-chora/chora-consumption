-- =============================================================================
-- chora-consumption : 0086_familiar_st1_band_progress_mirror.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Incubation Arc — F-I2 (CHO-2089, ADR-228 D3). The
--            EXP-gated hatch (F-I1) now AWARDS the Path's first Skill: species
--            Paths gain a stage-1 band of exactly ONE Skill (K=1), and
--            progress_mirror's min_growth_stage lowers 2 → 1 (pure verified-
--            profile read — free, no generation, no egress). The hatch ceremony
--            mints it AUTO-EQUIPPED (st1 carries 2 slots). Band arithmetic:
--            K = 1/3/4/6/7/6 (st1..st6) — the former st2 K=4 split into 1+3;
--            the full 27-Skill catalogue is still reached by st6 and every
--            st2-on cumulative total is UNCHANGED, so NO Skill except
--            progress_mirror changes band. GQ-6 (species-fixed order) stands.
--
--            This is the LIVE-DB DELTA for already-seeded databases: the 0061
--            catalogue + species_paths seed is INSERT ... ON CONFLICT DO
--            NOTHING, so re-applying the regenerated 0061 blocks never touches
--            existing rows. Fresh databases seed the new values directly at
--            0061; every already-seeded database converges here. The 0061
--            generated blocks were regenerated from the same seedspec fixtures
--            (the migration-content drift test proves they still agree).
--
-- Shape    : Standalone, idempotent UPDATEs — data, not schema (spec §5 idiom;
--            mirrors the 0063/0082/0085 activation migrations). No RLS toggle:
--            familiar_skill_catalog + species_paths are platform reference data
--            (no tenant axis), exactly as the activation flips UPDATE them.
--            Re-running is a no-op (same values). Reversible via the .down.sql.
--            Pinned to seedspec.Catalogue()/Paths() by
--            TestMigration0086_ReseedsExactlySeedspecStOneBand.
-- =============================================================================

BEGIN;

-- 1. progress_mirror drops to the st1 floor so ValidatePath admits it at
--    Path position 1 (the hatch-minted band). No other catalogue row moves.
UPDATE familiar_skill_catalog
   SET min_growth_stage = 1
 WHERE skill_key = 'progress_mirror'
   AND deleted_at IS NULL;

-- 2. Re-order each species' ACTIVE Path so it OPENS on progress_mirror (the
--    st1 band). The remaining st2 trio + every later entry keep their pre-F-I2
--    relative order, so only progress_mirror moved. dragon already opened on
--    progress_mirror (0061 seed) — its array is unchanged and it needs no row.
UPDATE species_paths
   SET entries = '["progress_mirror","explain_anew","recap_scribe","reminder_bell","worked_example","map_sight","quiz_me","weakness_sight","step_checker","socratic_drill","source_reader","flashcard_forge","path_weaver","polyglot","fact_check","web_research","long_weaving","photo_sight","study_calendar","goal_scribe","twin_rituals","watchful_eye","dawn_briefing","fog_scout","duel_second","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'owl' AND active;

UPDATE species_paths
   SET entries = '["progress_mirror","explain_anew","recap_scribe","reminder_bell","map_sight","quiz_me","polyglot","worked_example","fog_scout","goal_scribe","weakness_sight","photo_sight","socratic_drill","study_calendar","web_research","fact_check","source_reader","twin_rituals","long_weaving","path_weaver","watchful_eye","dawn_briefing","flashcard_forge","step_checker","duel_second","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'fox' AND active;

UPDATE species_paths
   SET entries = '["progress_mirror","reminder_bell","recap_scribe","explain_anew","quiz_me","worked_example","map_sight","polyglot","study_calendar","duel_second","weakness_sight","socratic_drill","flashcard_forge","goal_scribe","dawn_briefing","watchful_eye","twin_rituals","long_weaving","path_weaver","step_checker","source_reader","web_research","fact_check","photo_sight","fog_scout","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'penguin' AND active;

UPDATE species_paths
   SET entries = '["progress_mirror","recap_scribe","reminder_bell","explain_anew","weakness_sight","worked_example","quiz_me","step_checker","photo_sight","socratic_drill","path_weaver","flashcard_forge","map_sight","goal_scribe","dawn_briefing","watchful_eye","long_weaving","twin_rituals","web_research","study_calendar","source_reader","fact_check","fog_scout","duel_second","polyglot","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'phoenix' AND active;

COMMIT;

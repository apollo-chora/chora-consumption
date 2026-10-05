-- 0086 down: revert F-I2 (CHO-2089, ADR-228 D3). Restores progress_mirror to
-- the st2 floor and each species' ACTIVE Path to its pre-F-I2 order (the 0061
-- seed order, progress_mirror at its former st2-band position). Symmetric with
-- the up. NB: reverting the min_growth_stage without reverting the code bands
-- would leave a floor mismatch, so this down is only meaningful alongside a
-- code revert of DefaultStageUnlockCounts + the seedspec fixtures. Idempotent.

BEGIN;

UPDATE familiar_skill_catalog
   SET min_growth_stage = 2
 WHERE skill_key = 'progress_mirror'
   AND deleted_at IS NULL;

UPDATE species_paths
   SET entries = '["explain_anew","recap_scribe","progress_mirror","reminder_bell","worked_example","map_sight","quiz_me","weakness_sight","step_checker","socratic_drill","source_reader","flashcard_forge","path_weaver","polyglot","fact_check","web_research","long_weaving","photo_sight","study_calendar","goal_scribe","twin_rituals","watchful_eye","dawn_briefing","fog_scout","duel_second","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'owl' AND active;

UPDATE species_paths
   SET entries = '["explain_anew","recap_scribe","reminder_bell","progress_mirror","map_sight","quiz_me","polyglot","worked_example","fog_scout","goal_scribe","weakness_sight","photo_sight","socratic_drill","study_calendar","web_research","fact_check","source_reader","twin_rituals","long_weaving","path_weaver","watchful_eye","dawn_briefing","flashcard_forge","step_checker","duel_second","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'fox' AND active;

UPDATE species_paths
   SET entries = '["reminder_bell","recap_scribe","progress_mirror","explain_anew","quiz_me","worked_example","map_sight","polyglot","study_calendar","duel_second","weakness_sight","socratic_drill","flashcard_forge","goal_scribe","dawn_briefing","watchful_eye","twin_rituals","long_weaving","path_weaver","step_checker","source_reader","web_research","fact_check","photo_sight","fog_scout","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'penguin' AND active;

UPDATE species_paths
   SET entries = '["recap_scribe","reminder_bell","explain_anew","progress_mirror","weakness_sight","worked_example","quiz_me","step_checker","photo_sight","socratic_drill","path_weaver","flashcard_forge","map_sight","goal_scribe","dawn_briefing","watchful_eye","long_weaving","twin_rituals","web_research","study_calendar","source_reader","fact_check","fog_scout","duel_second","polyglot","atom_forge","weave_mastery"]'::jsonb
 WHERE species = 'phoenix' AND active;

COMMIT;

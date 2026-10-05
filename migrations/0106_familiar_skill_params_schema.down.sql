-- 0106_familiar_skill_params_schema.down.sql - reset the authored
-- params_schema rows to the 0061 dark-seed empty object.

UPDATE familiar_skill_catalog
SET params_schema = '{}'::jsonb
WHERE skill_key IN (
    'explain_anew',
    'fact_check',
    'fog_scout',
    'map_sight',
    'progress_mirror',
    'quiz_me',
    'recap_scribe',
    'socratic_drill',
    'weakness_sight',
    'web_research'
);

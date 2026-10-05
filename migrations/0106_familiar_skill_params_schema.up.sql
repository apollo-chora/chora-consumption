-- 0106_familiar_skill_params_schema.up.sql - CHO-2362 skills editor.
-- Authors the per-Skill params_schema JSONB (spec pack §2 sheets, §5.5
-- params model) for every Skill with an invoke-runner builder. DATA only:
-- generated from the domain sheet table (seedspec.ParamsSchemaMigrationSQL,
-- drift-pinned by the pg migration-content test); the release flag is a
-- separate vehicle and is not touched here.

UPDATE familiar_skill_catalog
SET params_schema = '{"target":{"type":"concept_ref","required":true},"style":{"type":"enum","values":["analogy","story","eli5","contrast","visual"],"default":"analogy"},"length":{"type":"enum","values":["short","full"],"default":"short"}}'::jsonb
WHERE skill_key = 'explain_anew';

UPDATE familiar_skill_catalog
SET params_schema = '{"claim":{"type":"text","maxLen":200,"required":true}}'::jsonb
WHERE skill_key = 'fact_check';

UPDATE familiar_skill_catalog
SET params_schema = '{"count":{"type":"int","min":3,"max":5,"default":"3"}}'::jsonb
WHERE skill_key = 'fog_scout';

UPDATE familiar_skill_catalog
SET params_schema = '{"focus":{"type":"concept_ref"}}'::jsonb
WHERE skill_key = 'map_sight';

UPDATE familiar_skill_catalog
SET params_schema = '{"window":{"type":"enum","values":["week","month","all"],"default":"all"}}'::jsonb
WHERE skill_key = 'progress_mirror';

UPDATE familiar_skill_catalog
SET params_schema = '{"scope":{"type":"enum","values":["weak","concept_ref","due"],"default":"weak"},"count":{"type":"int","min":3,"max":5,"default":"3"},"mode":{"type":"enum","values":["retrieve","generate"],"default":"retrieve"},"concept":{"type":"concept_ref"}}'::jsonb
WHERE skill_key = 'quiz_me';

UPDATE familiar_skill_catalog
SET params_schema = '{"session":{"type":"enum","values":["current","last"],"default":"current"}}'::jsonb
WHERE skill_key = 'recap_scribe';

UPDATE familiar_skill_catalog
SET params_schema = '{"scope":{"type":"enum","values":["weak","concept_ref"],"default":"weak"},"rounds":{"type":"int","min":3,"max":7,"default":"3"},"concept":{"type":"concept_ref"}}'::jsonb
WHERE skill_key = 'socratic_drill';

UPDATE familiar_skill_catalog
SET params_schema = '{"edge":{"type":"growth_edge_ref"}}'::jsonb
WHERE skill_key = 'weakness_sight';

UPDATE familiar_skill_catalog
SET params_schema = '{"direction":{"type":"text","maxLen":120,"required":true},"depth":{"type":"enum","values":["survey","deep"],"default":"survey"}}'::jsonb
WHERE skill_key = 'web_research';


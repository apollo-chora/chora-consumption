-- chora-consumption : 0057_goal_root_concept_and_personal_completion.down.sql
-- Reverse of 0057 up (schema only — the cert/course/path→curiosity data
-- conversion is not reversed).
BEGIN;

ALTER TABLE goals DROP CONSTRAINT IF EXISTS goals_kind_check;
ALTER TABLE goals
    ADD CONSTRAINT goals_kind_check
    CHECK (kind IN ('curiosity', 'cert', 'course', 'path', 'theme_mastery', 'edge'));

ALTER TABLE goals
    DROP COLUMN IF EXISTS personal_completed_at,
    DROP COLUMN IF EXISTS root_concept_id;

COMMIT;

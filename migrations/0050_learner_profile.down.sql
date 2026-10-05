-- =============================================================================
-- chora-consumption : 0050_learner_profile.down.sql  (reverse of 0050 up)
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS learner_activity_log;
DROP TABLE IF EXISTS learner_profile_facts;

COMMIT;

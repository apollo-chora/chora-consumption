-- 0048_learner_retention.down.sql — reverse of 0048_learner_retention.up.sql.
BEGIN;

DROP TABLE IF EXISTS learner_xp;
DROP TABLE IF EXISTS learner_streaks;
DROP TABLE IF EXISTS learner_seen_topics;
DROP TABLE IF EXISTS sm2_states;

COMMIT;

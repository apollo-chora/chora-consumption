-- =============================================================================
-- chora-consumption : 0046_learner_weakness.down.sql
--
-- Inverse of 0046_learner_weakness.up.sql — drops the table and all its indexes.
-- The RLS policy auto-drops with the table. The `vector` extension is left in
-- place (shared by familiar_memory_recall + chora-creation atom_embeddings).
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS learner_weakness;

COMMIT;

-- =============================================================================
-- chora-consumption : 0045_familiar_memory_recall.down.sql
--
-- Inverse of 0045_familiar_memory_recall.up.sql — drops the table and all its
-- indexes. RLS policy auto-drops with the table. The `vector` extension is left
-- in place (shared by chora-creation atom_embeddings; never dropped here).
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS familiar_memory_recall;

COMMIT;

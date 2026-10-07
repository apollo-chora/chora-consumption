-- Widen every chora-consumption embedding column to the embedding route's
-- native width.
--
-- The deployment's embedding route (registry id `text-embedding-004`) resolves
-- to LiquidAI's LFM2.5 embedding model on OpenRouter, which returns 1024-dim
-- vectors and REJECTS a `dimensions` override ("produces 1024-dimensional
-- embeddings"). The pgvector columns must match, and the gateway client sends
-- output_dimensions=1024 explicitly.
--
-- pgvector cannot cast between widths, so an ALTER on a POPULATED column
-- discards the stored vectors. This deployment has never persisted a companion
-- memory / learner embedding / learner-weakness concept vector (all three
-- tables are empty), which is what makes this a pure schema change; a populated
-- deployment would need a re-embed pass instead.
--
-- The ivfflat indexes are dropped and rebuilt because their operator classes are
-- bound to the column width. Recreated with the same shape as 0001_initial.sql /
-- 0046_learner_weakness.up.sql (companion_memory_recall is the table renamed
-- from familiar_memory_recall by 0110_companion_rename).
DROP INDEX IF EXISTS idx_learner_embeddings_cosine;
DROP INDEX IF EXISTS idx_companion_memory_recall_cosine;
DROP INDEX IF EXISTS idx_learner_weakness_cosine;

ALTER TABLE learner_embeddings       ALTER COLUMN embedding         TYPE vector(1024);
ALTER TABLE companion_memory_recall  ALTER COLUMN embedding         TYPE vector(1024);
ALTER TABLE learner_weakness         ALTER COLUMN concept_embedding TYPE vector(1024);

CREATE INDEX idx_learner_embeddings_cosine
    ON learner_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_companion_memory_recall_cosine
    ON companion_memory_recall USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_learner_weakness_cosine
    ON learner_weakness USING ivfflat (concept_embedding vector_cosine_ops)
    WITH (lists = 100);

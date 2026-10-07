-- Destructive rollback: 1024 -> 768 discards every stored embedding.
DROP INDEX IF EXISTS idx_learner_embeddings_cosine;
DROP INDEX IF EXISTS idx_companion_memory_recall_cosine;
DROP INDEX IF EXISTS idx_learner_weakness_cosine;

ALTER TABLE learner_embeddings       ALTER COLUMN embedding         TYPE vector(768);
ALTER TABLE companion_memory_recall  ALTER COLUMN embedding         TYPE vector(768);
ALTER TABLE learner_weakness         ALTER COLUMN concept_embedding TYPE vector(768);

CREATE INDEX idx_learner_embeddings_cosine
    ON learner_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_companion_memory_recall_cosine
    ON companion_memory_recall USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_learner_weakness_cosine
    ON learner_weakness USING ivfflat (concept_embedding vector_cosine_ops)
    WITH (lists = 100);

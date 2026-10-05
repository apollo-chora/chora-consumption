-- 0094_familiar_memory_source_metadata.down.sql
-- Reverses 0094. Dropping the column DISCARDS the grounded provenance of every
-- research note written since it was added — that provenance is not recoverable
-- from anywhere else (the citations' redirect uris have expired by design, and the
-- issued queries were never persisted elsewhere). Down-migrate only if you accept
-- losing it.

ALTER TABLE familiar_memory_recall
    DROP COLUMN IF EXISTS source_metadata;

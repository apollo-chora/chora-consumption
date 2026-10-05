-- =============================================================================
-- chora-consumption : 0093_learning_path_provenance.down.sql
--
-- Reverses 0093. Drops the ADR-233 provenance + traversal axes from
-- learning_paths.
--
-- ⚠ Data loss on down: collection-derived study lists lose the ONLY record of
-- where they came from (source_type/source_id) and of their dedupe anchor
-- (study_list_event_id). The paths + their atoms + learner progress SURVIVE
-- (the rows are untouched), but a re-convert after a down-migration would mint
-- a DUPLICATE path rather than additively re-syncing the existing one, because
-- the (tenant, owner, source_id) probe can no longer find it.
--
-- course_id / enrollment_id are NOT touched here — 0093 never dropped them
-- (they are the delivery binding, ADR-233 D2), so the course lane is unaffected.
-- =============================================================================

BEGIN;

DROP INDEX IF EXISTS idx_learning_paths_source;
DROP INDEX IF EXISTS idx_learning_paths_study_list_event;
DROP INDEX IF EXISTS idx_learning_paths_source_collection_owner;

ALTER TABLE learning_paths DROP CONSTRAINT IF EXISTS learning_paths_traversal_mode_check;
ALTER TABLE learning_paths DROP CONSTRAINT IF EXISTS learning_paths_source_type_check;

ALTER TABLE learning_paths DROP COLUMN IF EXISTS study_list_event_id;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS traversal_mode;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS source_id;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS source_type;

COMMIT;

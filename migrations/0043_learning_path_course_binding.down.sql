-- 0043 down — revert course-bound LearningPath durability columns.
-- NB: the cardinality(atom_ids) >= 1 CHECK is intentionally NOT recreated —
-- 0-atom paths remain valid (re-adding it would reject already-bootstrapped
-- content-empty paths). Recreate manually only after purging 0-atom rows.

DROP INDEX IF EXISTS idx_learning_paths_course_owner;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS current_index;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS enrollment_id;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS course_id;

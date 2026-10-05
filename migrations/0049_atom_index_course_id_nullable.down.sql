-- 0049_atom_index_course_id_nullable.down.sql — reverse of the up migration.
--
-- Rolling back re-imposes NOT NULL. Standalone-atom rows (course_id IS NULL)
-- would block the constraint — they are soft-deleted first (deleted_at) so the
-- rollback never destroys projection rows outright; a later re-up + event
-- replay (or re-PATCH re-emit) restores them.
BEGIN;

UPDATE atom_index SET deleted_at = now() WHERE course_id IS NULL AND deleted_at IS NULL;
UPDATE atom_index SET course_id = '00000000-0000-0000-0000-000000000000' WHERE course_id IS NULL;
ALTER TABLE atom_index ALTER COLUMN course_id SET NOT NULL;

COMMIT;

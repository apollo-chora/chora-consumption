-- 0072_course_directory.down.sql — drop the course_id → title projection.
-- A derived read-model (no system-of-record data), so a hard DROP is safe on
-- rollback; the projection re-hydrates from chora.delivery.course.{created,
-- updated}.v1 replays after a re-apply.
DROP TABLE IF EXISTS course_directory;

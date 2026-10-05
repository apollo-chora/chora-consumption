-- 0072_course_directory.up.sql — tenant-agnostic course_id → title projection
-- (chora-consumption side; completes the CHO-2059 Familiar-course-name follow-up).
--
-- Purpose:
--   A learner-facing Familiar reflects the learner's verified LearnerProfile
--   facts (enrolments / completions / certifications), which carry raw
--   cross-domain course_id UUIDs and NO course name — chora-consumption owns no
--   courses (they live in chora_delivery) and cross-DB queries are FORBIDDEN
--   across the 13-DB topology (.claude/rules/ddd-enforcement.md HARD RULE #1).
--   So the Familiar could only say "a course". This table is a local read-model,
--   updated over Pub/Sub from chora.delivery.course.created.v1 +
--   chora.delivery.course.updated.v1
--   (internal/adapter/subscribers/course_metadata_subscriber.go). The
--   learner_profile presenter (FactDisplayLabel / ActivityDisplay) then
--   stitches this title onto the learner's OWN RLS-scoped facts so the Familiar
--   can name the real course ("Algebra I"). Mirrors chora-delivery's
--   user_directory (the Q3 GCID → display-name projection) one domain over.
--
-- TENANT-AGNOSTIC + NO RLS — deliberate, justified:
--   The key is the GLOBAL course_id (opaque UUIDv7, no tenant context embedded);
--   a course title is not tenant-scoped (one course, one title). So there is no
--   tenant_id column and no RLS policy. Isolation is preserved at the JOIN
--   boundary: a title is only ever surfaced by stitching against the learner's
--   OWN RLS-scoped LearnerProfile facts, so a caller resolves a title only for a
--   course_id already visible in their own tenant's facts. RLS here would be
--   meaningless (no tenant column to scope on) and would break the global
--   lookup. This is NOT one of the two ADR-scoped RLS-bypass surfaces
--   (ADR-165 / ADR-184) — it is a table that never carried tenant data.
--
-- Last-writer-wins: reconciliation on updated_at happens in the app
--   (pg.SQLUpsertCourseDirectory: DO UPDATE ... WHERE course_directory.updated_at
--   < EXCLUDED.updated_at), so an out-of-order redelivery cannot clobber a newer
--   title and an equal-timestamp replay no-ops. No soft-delete column — this is a
--   derived projection, not a system of record.
--
-- Grants: explicit per-DB app-role grants. These MUST target
--   chora_consumption_app_rw / chora_consumption_app_ro — a global app_rw role
--   does NOT exist in chora_consumption and would abort + roll back the whole
--   migration. (ALTER DEFAULT PRIVILEGES in 9999_grant_app_roles.sql also covers
--   this table; the explicit grants are belt-and-suspenders + self-documenting.)
--   DELETE is deliberately withheld — never hard-delete.

CREATE TABLE IF NOT EXISTS course_directory (
    course_id  UUID        PRIMARY KEY,
    title      TEXT        NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-DB app-role grants (MUST be chora_consumption_app_rw / _app_ro). The
-- upsert path needs SELECT/INSERT/UPDATE; DELETE is intentionally not granted.
GRANT SELECT, INSERT, UPDATE ON course_directory TO chora_consumption_app_rw;
GRANT SELECT                 ON course_directory TO chora_consumption_app_ro;

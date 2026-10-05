-- 0042_course_content_projection.up.sql
--
-- CHO-1612 D2 — durable course-content read projection.
--
-- chora-consumption projects chora.delivery.course.content_composed.v1 (a FULL
-- ordered curriculum snapshot per course) so the A+ open-course learn page can
-- render the heterogeneous curriculum (atoms / videos / youtube / documents /
-- live classrooms / assessments). Previously held in-memory per-pod — lost on
-- restart + split across replicas, causing transient empty reads during deploy
-- churn. This table makes it durable + replica-consistent.
--
-- Read-side projection: chora-consumption NEVER owns course content (atom-
-- centric — an item REFERENCES an atom/assessment/classroom by id or external
-- media by URL via `ref`). Source of truth is chora-delivery's course_content
-- aggregate + the replayable event stream. Cross-DB queries FORBIDDEN — fed by
-- Pub/Sub only (per ddd-enforcement).
--
-- Per multi-tenant-rls SKILL: tenant-scoped table = RLS policy on
-- current_setting('chora.tenant_id', true)::uuid. Tenant-scoped only (course
-- content is a tenant asset, not per-user data).
--
-- Soft delete (ddd-enforcement): the pg adapter's ReplaceByCourse tombstones a
-- course's live rows (deleted_at = NOW()) then upsert-revives the new snapshot
-- (deleted_at = NULL). Reads filter deleted_at IS NULL.

CREATE TABLE course_content (
    item_id      UUID         PRIMARY KEY,                 -- chora-delivery content-item id
    tenant_id    UUID         NOT NULL,
    course_id    UUID         NOT NULL,                    -- cross-DB ref to chora_delivery
    kind         VARCHAR(32)  NOT NULL,                    -- atom|video|youtube|document|live_classroom|assessment
    ref          VARCHAR(2048) NOT NULL,                   -- atom/assessment/classroom id OR external media URL
    title        VARCHAR(512) NOT NULL DEFAULT '',
    position     INTEGER      NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ  NULL
);

-- Ordered read by (tenant, course); excludes tombstones.
CREATE INDEX idx_course_content_course
    ON course_content (tenant_id, course_id, position)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_course_content_updated_at
    BEFORE UPDATE ON course_content
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE course_content ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON course_content
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- =============================================================================
-- chora-consumption : 0004_topic_accuracy_active_path.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : agent-a5a41ed7b4e1d71fb (S6.4 A-Consumption-Wiring)
-- Date          : 2026-05-09
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1) +
--                 ADR-143 per-user KG continued
--
-- Adds the projection tables that A-Familiar's daily-dose composer
-- reads from (40/30/30 split). Production cutover from in-memory repos
-- happens in M12 — until then, both surfaces are populated by the
-- subscribers wired in services/chora-consumption/internal/adapter/
-- subscribers/{topic_accuracy_subscriber.go, active_path_topics_subscriber.go}.
--
-- Tables added in this migration:
--
--   1. topic_retention   — per-(tenant, gcid, topic_id) Ebbinghaus
--                          retention strength + last-reviewed timestamp.
--                          Read path: dose composer's Ebbinghaus slot.
--                          Write path: SessionCompletionHandler.
--
--   2. atom_index        — skinny per-(tenant, course, atom_id)
--                          projection from chora.creation.atom.created.v1.
--                          Read path: atom topic resolution + MCQ grade.
--                          Write path: AtomCreatedSubscriber.
--
--   3. topic_accuracy    — per-(tenant, gcid, topic_tag) MCQ rolling
--                          counters (attempts + correct).
--                          Read path: dose composer's WEAKNESS slot.
--                          Write path: TopicAccuracySubscriber.
--
--   4. active_path_topics — per-(tenant, gcid) topic-set covered by
--                          learner's active LearningPath(s).
--                          Read path: dose composer's CURIOSITY slot.
--                          Write path: ActivePathTopicsSubscriber.
--
-- Per ddd-enforcement #5: every table soft-delete via deleted_at.
-- Per multi-tenant-rls SKILL: every tenant-scoped table = RLS policy
-- on `current_setting('chora.tenant_id', true)::uuid`. User-scoped
-- (gcid) tables ALSO carry user_isolation policy on
-- `current_setting('chora.user_gcid', true)::uuid` (matches the
-- 0002/0003 KG migrations for shape consistency).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- topic_retention — per-(tenant, gcid, topic_id) Ebbinghaus state
--
-- Domain mirror: services/chora-consumption/internal/domain/topic_retention/
-- topic_retention.go (TopicScore aggregate).
-- -----------------------------------------------------------------------------

CREATE TABLE topic_retention (
    score_id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),  -- UUIDv7 in app code
    tenant_id          UUID         NOT NULL,
    gcid               UUID         NOT NULL,
    topic_id           VARCHAR(128) NOT NULL,                              -- topic_tag string (NOT a UUID)
    strength           DOUBLE PRECISION NOT NULL DEFAULT 1.0 CHECK (strength >= 0.5 AND strength <= 365.0),
    retention_score    DOUBLE PRECISION NOT NULL DEFAULT 1.0 CHECK (retention_score >= 0.0 AND retention_score <= 1.0),
    review_count       INTEGER      NOT NULL DEFAULT 0 CHECK (review_count >= 0),
    last_reviewed_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ  NULL,
    UNIQUE (tenant_id, gcid, topic_id)
);

CREATE INDEX idx_topic_retention_learner
    ON topic_retention (tenant_id, gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_topic_retention_due
    ON topic_retention (tenant_id, gcid, last_reviewed_at)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_topic_retention_updated_at
    BEFORE UPDATE ON topic_retention
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE topic_retention ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON topic_retention
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON topic_retention
    FOR ALL USING (
        gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- atom_index — skinny per-(tenant, course, atom_id) projection
--
-- Hydrated from chora.creation.atom.created.v1 events (cross-DB-forbidden
-- per ddd-enforcement HARD RULE). Domain mirror: services/chora-consumption/
-- internal/domain/atom_index/atom_index.go.
-- -----------------------------------------------------------------------------

CREATE TABLE atom_index (
    atom_id            UUID         PRIMARY KEY,                            -- cross-DB ref to chora_creation
    tenant_id          UUID         NOT NULL,
    course_id          UUID         NOT NULL,
    title              VARCHAR(512) NOT NULL DEFAULT '',
    atom_type          VARCHAR(32)  NOT NULL DEFAULT 'unknown',
    difficulty         INTEGER      NOT NULL DEFAULT 0,
    topic_tags         TEXT[]       NOT NULL DEFAULT '{}',
    correct_index      INTEGER      NULL,                                   -- nil for non-MCQ
    answer_count       INTEGER      NOT NULL DEFAULT 0,
    published_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ  NULL
);

CREATE INDEX idx_atom_index_course
    ON atom_index (tenant_id, course_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_atom_index_tags_gin
    ON atom_index USING GIN (topic_tags)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_atom_index_updated_at
    BEFORE UPDATE ON atom_index
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE atom_index ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_index
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
-- atom_index is tenant-scoped only (no per-user filter — atoms are tenant
-- assets, not per-user data).

-- -----------------------------------------------------------------------------
-- topic_accuracy — per-(tenant, gcid, topic_tag) MCQ rolling counters
--
-- Subscriber populates this on every chora.consumption.atom_session.
-- completed.v1 with is_correct flag. Read path: A-Familiar dose
-- composer's WEAKNESS slot (accuracy < threshold ⇒ weakness pick).
-- -----------------------------------------------------------------------------

CREATE TABLE topic_accuracy (
    accuracy_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),  -- UUIDv7 in app code
    tenant_id          UUID         NOT NULL,
    gcid               UUID         NOT NULL,
    topic_tag          VARCHAR(128) NOT NULL,
    attempts           INTEGER      NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    correct            INTEGER      NOT NULL DEFAULT 0 CHECK (correct >= 0 AND correct <= attempts),
    last_attempted_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ  NULL,
    UNIQUE (tenant_id, gcid, topic_tag)
);

CREATE INDEX idx_topic_accuracy_learner
    ON topic_accuracy (tenant_id, gcid)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_topic_accuracy_updated_at
    BEFORE UPDATE ON topic_accuracy
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE topic_accuracy ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON topic_accuracy
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON topic_accuracy
    FOR ALL USING (
        gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- topic_accuracy_session_dedup — domain-level idempotency for
-- (gcid, atom_session_id). Per the S6.4 brief: re-firing an event with
-- the same atom_session_id MUST be a no-op (envelope event_id idempotency
-- only catches at-least-once Pub/Sub retries, not republished events
-- with fresh event_ids that touch the same domain entity).
-- -----------------------------------------------------------------------------

CREATE TABLE topic_accuracy_session_dedup (
    tenant_id          UUID         NOT NULL,
    gcid               UUID         NOT NULL,
    atom_session_id    UUID         NOT NULL,
    recorded_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gcid, atom_session_id)
);

ALTER TABLE topic_accuracy_session_dedup ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON topic_accuracy_session_dedup
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON topic_accuracy_session_dedup
    FOR ALL USING (
        gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- active_path_topics — per-(tenant, gcid, learning_path_id, topic_tag)
-- topic-set covered by the learner's active LearningPath(s).
--
-- One row per (gcid, path, topic). Subscriber populates via
-- learning_path.bootstrapped.v1 (insert) + advanced.v1 (current_position
-- update; idempotent on the (gcid, path, topic) PK).
-- -----------------------------------------------------------------------------

CREATE TABLE active_path_topics (
    tenant_id          UUID         NOT NULL,
    gcid               UUID         NOT NULL,
    learning_path_id   UUID         NOT NULL,
    topic_tag          VARCHAR(128) NOT NULL,
    current_position   INTEGER      NOT NULL DEFAULT 0,
    explored           BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ  NULL,
    PRIMARY KEY (tenant_id, gcid, learning_path_id, topic_tag)
);

CREATE INDEX idx_active_path_topics_learner
    ON active_path_topics (tenant_id, gcid)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_active_path_topics_updated_at
    BEFORE UPDATE ON active_path_topics
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE active_path_topics ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON active_path_topics
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON active_path_topics
    FOR ALL USING (
        gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

COMMIT;

-- =============================================================================
-- Down migration (rollback) — informational only; pre-M12 we lean on PITR
-- =============================================================================
-- BEGIN;
--   DROP TABLE IF EXISTS active_path_topics;
--   DROP TABLE IF EXISTS topic_accuracy_session_dedup;
--   DROP TABLE IF EXISTS topic_accuracy;
--   DROP TABLE IF EXISTS atom_index;
--   DROP TABLE IF EXISTS topic_retention;
-- COMMIT;

-- =============================================================================
-- chora-consumption : 0050_learner_profile.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : Familiar Holistic Redesign WS1 — Global LearnerProfile
--                 read-model (ADR-200), CHO-1908, 2026-06-28
-- Architecture  : docs/architecture/adrs/adr-200-familiar-global-learner-profile-read-model.md
--                 internal/domain/learner_profile/* (aggregate + ProjectionRepo)
--
-- Purpose:
--   Per-GCID global LearnerProfile projection — the Familiar's verified view of
--   what a learner has actually done across Chora (enrolments, course
--   completions, certifications, assessment results, identity preferences) plus
--   an append-only activity/decision log for conversational recall.
--
--   PROJECTION, not a write-model: chora-consumption owns none of this content —
--   it is fed ONLY by verified Pub/Sub events (delivery.course.completed,
--   delivery.certification.issued, {delivery|creation}.assessment.graded,
--   consumption.path.completed, identity.preferences.updated). Cross-DB queries
--   are FORBIDDEN (ddd-enforcement #1).
--
--   VERIFIED-ONLY (ADR-203 / L16, anti-gaming): every row carries a
--   source_event_id — the Chora system event that proves it. This same stream
--   is the un-gameable familiar-EXP source + FamiliarGoal achievement feed.
--
-- Soft-delete only (`deleted_at`) per ddd-enforcement #5. RLS-enabled (every
-- read/write runs rls.ApplySession first). UUID default; UUIDv7 minted in the
-- domain layer.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- learner_profile_facts — one row per verified fact. Natural identity is
-- (tenant, learner, fact_type, ref) so a redelivered event upserts in place
-- (idempotent) and a re-occurrence updates the row. ref_id is a cross-domain
-- reference (course/cert/assessment/path id, or a preference key) — opaque,
-- no FK (ddd-enforcement #3). detail is distilled metadata (label / score /
-- passed / hint_count / extra), NEVER the source aggregate.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_profile_facts (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID         NOT NULL,
    learner_gcid    UUID         NOT NULL,
    fact_type       VARCHAR(32)  NOT NULL
                                 CHECK (fact_type IN (
                                     'enrollment', 'course_completed', 'certification_issued',
                                     'assessment_graded', 'path_completed', 'preference')),
    ref_id          TEXT         NOT NULL,
    detail          JSONB        NOT NULL DEFAULT '{}'::jsonb,
    source_event_id UUID         NOT NULL,                       -- verified-only anchor (ADR-203)
    occurred_at     TIMESTAMPTZ  NOT NULL,
    recorded_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ                                  -- soft-delete (ddd-enforcement #5)
);

-- Idempotent-upsert identity: ON CONFLICT (tenant, learner, fact_type, ref_id).
CREATE UNIQUE INDEX IF NOT EXISTS uq_learner_profile_facts_natural
    ON learner_profile_facts (tenant_id, learner_gcid, fact_type, ref_id);

-- Hot path: per-learner live-row scan (BuildView reads all live facts).
CREATE INDEX IF NOT EXISTS idx_learner_profile_facts_learner
    ON learner_profile_facts (tenant_id, learner_gcid) WHERE deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- learner_activity_log — append-only "completed X / scored Y / skipped Z" log.
-- Deduped on (tenant, source_event_id) so a redelivered event logs once.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_activity_log (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID         NOT NULL,
    learner_gcid    UUID         NOT NULL,
    kind            VARCHAR(48)  NOT NULL,
    summary         TEXT         NOT NULL,
    ref_id          TEXT         NOT NULL DEFAULT '',
    source_event_id UUID         NOT NULL,
    occurred_at     TIMESTAMPTZ  NOT NULL,
    recorded_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_learner_activity_log_event
    ON learner_activity_log (tenant_id, source_event_id);

-- Recent-first per-learner read.
CREATE INDEX IF NOT EXISTS idx_learner_activity_log_recent
    ON learner_activity_log (tenant_id, learner_gcid, occurred_at DESC);

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- Every read/write runs SET LOCAL chora.tenant_id (rls.ApplySession) BEFORE the
-- user query; per-learner scoping is an explicit learner_gcid predicate.
-- -----------------------------------------------------------------------------
ALTER TABLE learner_profile_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_profile_facts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_profile_facts;
CREATE POLICY tenant_isolation ON learner_profile_facts
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

ALTER TABLE learner_activity_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_activity_log FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_activity_log;
CREATE POLICY tenant_isolation ON learner_activity_log
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by 9999_grant_app_roles.sql (re-runs lex-last; ALTER
-- DEFAULT PRIVILEGES grants app_rw / app_ro on new tables automatically).

COMMIT;

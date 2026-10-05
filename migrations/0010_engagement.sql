-- =============================================================================
-- chora-consumption : 0010_engagement.sql
--
-- Consolidates `chora-engagement` legacy migrations into chora-consumption per
-- M12.2 Batch 3.a. Engagement primitives (Streak, XP, Goal, DailyDose,
-- Leaderboard, Notifications, Fog discovery, StarAccount, PredictedGrade,
-- DataPipeline configuration) now live in the Content Consumption domain.
--
-- Source: chora-engagement/migrations/001-015 (up only)
-- Domain: Content Consumption (5 core)
-- Database: chora_consumption
-- RLS: tenant-scoped on every tenant_id-bearing table (preserved verbatim)
-- HARD INVARIANT: cross-DB queries forbidden — `gcid` is cross-context UUID
--                 (no FK to chora_identity; validated via Pub/Sub events).
-- =============================================================================

-- NOTE: chora-consumption already creates pgcrypto / uuid-ossp / update_updated_at()
-- in 0001_initial.sql. The original engagement bootstrap migration re-creates
-- those; we guard with IF NOT EXISTS / CREATE OR REPLACE so the consolidated
-- file is idempotent against any existing chora_consumption schema state.

BEGIN;

-- ==========================================================================
-- Migration: 001_create_extensions_and_enums.up.sql (engagement consolidation)
-- ==========================================================================
-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-engagement.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Streak lifecycle status.
-- active: learner completed an atom today
-- at_risk: no activity today, but streak not yet broken
-- broken: learner missed a day; streak resets
CREATE TYPE streak_status AS ENUM (
    'active',
    'at_risk',
    'broken'
);

-- XP combo multiplier tier.
-- Escalates on consecutive correct answers: base(1x) → bronze(2x) → silver(3x) → gold(4x).
-- An incorrect answer resets to base.
CREATE TYPE combo_tier AS ENUM (
    'base',
    'bronze',
    'silver',
    'gold'
);

-- Goal challenge lifecycle.
CREATE TYPE goal_status AS ENUM (
    'pending',
    'active',
    'completed',
    'expired',
    'declined'
);

-- Goal measurement target type.
CREATE TYPE goal_target_type AS ENUM (
    'atom_count',
    'retention_pct',
    'path_completion'
);

-- DailyDose session status.
CREATE TYPE daily_dose_status AS ENUM (
    'available',
    'completed',
    'not_configured'
);

-- Engagement notification types.
CREATE TYPE notification_type AS ENUM (
    'daily_dose',
    'streak_alert',
    'goal_deadline',
    'goal_completed',
    'level_up',
    'skin_unlocked',
    'inactivity'
);

-- Source of XP earning.
CREATE TYPE xp_source AS ENUM (
    'atom_correct',
    'atom_incorrect',
    'daily_dose_bonus',
    'goal_completed',
    'streak_milestone',
    'new_topic'
);

-- Leaderboard period.
CREATE TYPE leaderboard_period AS ENUM (
    'weekly',
    'monthly',
    'all_time'
);

-- Leaderboard scope.
CREATE TYPE leaderboard_scope AS ENUM (
    'tenant',
    'topic',
    'path'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ==========================================================================
-- Migration: 002_create_streaks.up.sql (engagement consolidation)
-- ==========================================================================
-- 002_create_streaks.up.sql
-- Streak — per-GCID per-tenant daily learning streak tracker.
-- gcid is a cross-context UUID reference to chora-iam (no FK per DDD rule #3).
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE streaks (
    id              UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid            UUID          NOT NULL,  -- cross-context reference, no FK
    tenant_id       UUID          NOT NULL,  -- cross-context reference, no FK
    current_days    INTEGER       NOT NULL DEFAULT 0,
    longest_streak  INTEGER       NOT NULL DEFAULT 0,
    status          streak_status NOT NULL DEFAULT 'broken',
    last_activity_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- One streak record per GCID per tenant.
CREATE UNIQUE INDEX uq_streaks_gcid_tenant
    ON streaks (gcid, tenant_id);

-- Tenant-scoped queries.
CREATE INDEX idx_streaks_tenant_id
    ON streaks (tenant_id);

-- Lookup by GCID across tenants (admin).
CREATE INDEX idx_streaks_gcid
    ON streaks (gcid);

-- Auto-update updated_at.
CREATE TRIGGER trg_streaks_updated_at
    BEFORE UPDATE ON streaks
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Tenant-scoped RLS (DP-02).
ALTER TABLE streaks ENABLE ROW LEVEL SECURITY;
ALTER TABLE streaks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON streaks
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 003_create_streak_milestones.up.sql (engagement consolidation)
-- ==========================================================================
-- 003_create_streak_milestones.up.sql
-- StreakMilestone — records when a learner reached a streak milestone (7, 30, 100, 365 days).
-- Milestones trigger DigitalSkin unlocks and XP bonuses.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE streak_milestones (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid        UUID        NOT NULL,  -- cross-context reference, no FK
    tenant_id   UUID        NOT NULL,  -- cross-context reference, no FK
    days        INTEGER     NOT NULL,
    reward_type VARCHAR(50) NOT NULL,
    reached_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Each GCID can reach each milestone once per tenant.
CREATE UNIQUE INDEX uq_streak_milestones_gcid_tenant_days
    ON streak_milestones (gcid, tenant_id, days);

-- Tenant-scoped queries.
CREATE INDEX idx_streak_milestones_tenant_id
    ON streak_milestones (tenant_id);

-- Lookup milestones by GCID and tenant (streak detail page).
CREATE INDEX idx_streak_milestones_gcid_tenant
    ON streak_milestones (gcid, tenant_id);

-- Tenant-scoped RLS (DP-02).
ALTER TABLE streak_milestones ENABLE ROW LEVEL SECURITY;
ALTER TABLE streak_milestones FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON streak_milestones
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 004_create_xp_ledgers.up.sql (engagement consolidation)
-- ==========================================================================
-- 004_create_xp_ledgers.up.sql
-- XPLedger — per-GCID per-tenant XP total, level, and combo tier.
-- gcid is a cross-context UUID reference to chora-iam (no FK per DDD rule #3).
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE xp_ledgers (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid        UUID        NOT NULL,  -- cross-context reference, no FK
    tenant_id   UUID        NOT NULL,  -- cross-context reference, no FK
    total_xp    INTEGER     NOT NULL DEFAULT 0,
    level       INTEGER     NOT NULL DEFAULT 1,
    combo_tier  combo_tier  NOT NULL DEFAULT 'base',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One XP ledger per GCID per tenant.
CREATE UNIQUE INDEX uq_xp_ledgers_gcid_tenant
    ON xp_ledgers (gcid, tenant_id);

-- Tenant-scoped queries.
CREATE INDEX idx_xp_ledgers_tenant_id
    ON xp_ledgers (tenant_id);

-- Leaderboard ranking queries (XP desc within tenant).
CREATE INDEX idx_xp_ledgers_tenant_total_xp
    ON xp_ledgers (tenant_id, total_xp DESC);

-- Auto-update updated_at.
CREATE TRIGGER trg_xp_ledgers_updated_at
    BEFORE UPDATE ON xp_ledgers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Tenant-scoped RLS (DP-02).
ALTER TABLE xp_ledgers ENABLE ROW LEVEL SECURITY;
ALTER TABLE xp_ledgers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON xp_ledgers
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 005_create_xp_entries.up.sql (engagement consolidation)
-- ==========================================================================
-- 005_create_xp_entries.up.sql
-- XPEntry — append-only record of individual XP earning events.
-- Per DDD rule #4 (AtomRevision pattern): NEVER UPDATE or DELETE.
-- No updated_at or deleted_at columns — immutable once written.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE xp_entries (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid             UUID        NOT NULL,  -- cross-context reference, no FK
    tenant_id        UUID        NOT NULL,  -- cross-context reference, no FK
    xp_earned        INTEGER     NOT NULL,
    source           xp_source   NOT NULL,
    atom_id          UUID,                  -- nullable; cross-context reference, no FK
    combo_multiplier INTEGER     NOT NULL DEFAULT 1,
    earned_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tenant-scoped queries.
CREATE INDEX idx_xp_entries_tenant_id
    ON xp_entries (tenant_id);

-- XP history by GCID within tenant (XP detail page, paginated by earned_at).
CREATE INDEX idx_xp_entries_gcid_tenant_earned
    ON xp_entries (gcid, tenant_id, earned_at DESC);

-- Source-filtered queries (e.g., show only atom_correct entries).
CREATE INDEX idx_xp_entries_tenant_source
    ON xp_entries (tenant_id, source);

-- Tenant-scoped RLS (DP-02).
ALTER TABLE xp_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE xp_entries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON xp_entries
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 006_create_goal_challenges.up.sql (engagement consolidation)
-- ==========================================================================
-- 006_create_goal_challenges.up.sql
-- GoalChallenge — instructor/parent-assigned goals with bounty star credits.
-- Max 3 concurrent active goals per learner (enforced in domain service).
-- Soft delete via deleted_at (DDD rule #5).
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE goal_challenges (
    id                  UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID          NOT NULL,  -- cross-context reference, no FK
    assignee_gcid       UUID          NOT NULL,  -- cross-context reference, no FK
    created_by          UUID          NOT NULL,  -- cross-context reference, no FK
    title               VARCHAR(200)  NOT NULL,
    description         TEXT          NOT NULL DEFAULT '',
    target_scope        JSONB         NOT NULL,  -- GoalTargetScope: {type, topic_id?, target_value}
    deadline            TIMESTAMPTZ   NOT NULL,
    bounty_star_credits INTEGER       NOT NULL DEFAULT 0,
    status              goal_status   NOT NULL DEFAULT 'pending',
    progress_pct        NUMERIC(5,2)  NOT NULL DEFAULT 0.00,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ            -- soft delete (DDD rule #5)
);

-- Tenant-scoped queries.
CREATE INDEX idx_goal_challenges_tenant_id
    ON goal_challenges (tenant_id);

-- Active goals for a learner (enforcing max-3 check).
CREATE INDEX idx_goal_challenges_assignee_status
    ON goal_challenges (assignee_gcid, tenant_id, status)
    WHERE deleted_at IS NULL;

-- Goals by creator (instructor dashboard).
CREATE INDEX idx_goal_challenges_created_by
    ON goal_challenges (created_by, tenant_id)
    WHERE deleted_at IS NULL;

-- Expiry check: pending/active goals past deadline.
CREATE INDEX idx_goal_challenges_deadline
    ON goal_challenges (deadline)
    WHERE status IN ('pending', 'active') AND deleted_at IS NULL;

-- Auto-update updated_at.
CREATE TRIGGER trg_goal_challenges_updated_at
    BEFORE UPDATE ON goal_challenges
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Tenant-scoped RLS (DP-02).
ALTER TABLE goal_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE goal_challenges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON goal_challenges
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 007_create_daily_dose_sessions.up.sql (engagement consolidation)
-- ==========================================================================
-- 007_create_daily_dose_sessions.up.sql
-- DailyDoseSession — AI-curated daily review session.
-- Atoms stored as JSONB array (cross-context snapshot, not FK).
-- Composition stored as JSONB (ebbinghaus_pct, curiosity_pct, weakness_pct, goal_overlay_pct).
-- No updated_at — sessions are completed once and not modified.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE daily_dose_sessions (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid               UUID        NOT NULL,  -- cross-context reference, no FK
    tenant_id          UUID        NOT NULL,  -- cross-context reference, no FK
    atoms              JSONB       NOT NULL DEFAULT '[]'::jsonb,
    composition        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    estimated_minutes  INTEGER     NOT NULL DEFAULT 0,
    goal_aligned_count INTEGER     NOT NULL DEFAULT 0,
    atoms_completed    INTEGER     NOT NULL DEFAULT 0,
    atoms_correct      INTEGER     NOT NULL DEFAULT 0,
    total_xp_earned    INTEGER     NOT NULL DEFAULT 0,
    completed_at       TIMESTAMPTZ,          -- null until session is completed
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tenant-scoped queries.
CREATE INDEX idx_daily_dose_sessions_tenant_id
    ON daily_dose_sessions (tenant_id);

-- Today's session lookup (most recent per GCID per tenant).
CREATE INDEX idx_daily_dose_sessions_gcid_tenant_created
    ON daily_dose_sessions (gcid, tenant_id, created_at DESC);

-- Incomplete sessions (session management).
CREATE INDEX idx_daily_dose_sessions_incomplete
    ON daily_dose_sessions (gcid, tenant_id)
    WHERE completed_at IS NULL;

-- Tenant-scoped RLS (DP-02).
ALTER TABLE daily_dose_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE daily_dose_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON daily_dose_sessions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 008_create_leaderboard_snapshots.up.sql (engagement consolidation)
-- ==========================================================================
-- 008_create_leaderboard_snapshots.up.sql
-- LeaderboardSnapshot — read model for leaderboard rankings.
-- Materialized periodically (weekly/monthly/all_time) by a background job.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE leaderboard_snapshots (
    id            UUID              PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID              NOT NULL,  -- cross-context reference, no FK
    gcid          UUID              NOT NULL,  -- cross-context reference, no FK
    period        leaderboard_period NOT NULL,
    scope         leaderboard_scope  NOT NULL,
    scope_id      UUID,                        -- topic_id or path_id when scope != 'tenant'
    total_xp      INTEGER           NOT NULL DEFAULT 0,
    level         INTEGER           NOT NULL DEFAULT 1,
    streak_days   INTEGER           NOT NULL DEFAULT 0,
    rank          INTEGER           NOT NULL,
    snapshot_date DATE              NOT NULL,
    created_at    TIMESTAMPTZ       NOT NULL DEFAULT now()
);

-- One entry per GCID per period/scope/date within a tenant.
CREATE UNIQUE INDEX uq_leaderboard_snapshots_entry
    ON leaderboard_snapshots (tenant_id, gcid, period, scope, COALESCE(scope_id, '00000000-0000-0000-0000-000000000000'::uuid), snapshot_date);

-- Tenant-scoped queries.
CREATE INDEX idx_leaderboard_snapshots_tenant_id
    ON leaderboard_snapshots (tenant_id);

-- Leaderboard page query: tenant + period + scope + date, ordered by rank.
CREATE INDEX idx_leaderboard_snapshots_query
    ON leaderboard_snapshots (tenant_id, period, scope, snapshot_date, rank);

-- Learner's rank lookup.
CREATE INDEX idx_leaderboard_snapshots_gcid
    ON leaderboard_snapshots (gcid, tenant_id, period, snapshot_date);

-- Tenant-scoped RLS (DP-02).
ALTER TABLE leaderboard_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE leaderboard_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON leaderboard_snapshots
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 009_create_engagement_notifications.up.sql (engagement consolidation)
-- ==========================================================================
-- 009_create_engagement_notifications.up.sql
-- EngagementNotification — learner-facing notifications for engagement events.
-- Types: daily_dose, streak_alert, goal_deadline, goal_completed, level_up, skin_unlocked, inactivity.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE engagement_notifications (
    id          UUID              PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid        UUID              NOT NULL,  -- cross-context reference, no FK
    tenant_id   UUID              NOT NULL,  -- cross-context reference, no FK
    type        notification_type NOT NULL,
    title       VARCHAR(200)      NOT NULL,
    message     TEXT              NOT NULL DEFAULT '',
    read        BOOLEAN           NOT NULL DEFAULT false,
    action_url  TEXT,
    created_at  TIMESTAMPTZ       NOT NULL DEFAULT now()
);

-- Tenant-scoped queries.
CREATE INDEX idx_engagement_notifications_tenant_id
    ON engagement_notifications (tenant_id);

-- Notification feed: GCID within tenant, newest first.
CREATE INDEX idx_engagement_notifications_feed
    ON engagement_notifications (gcid, tenant_id, created_at DESC);

-- Unread count badge.
CREATE INDEX idx_engagement_notifications_unread
    ON engagement_notifications (gcid, tenant_id)
    WHERE read = false;

-- Tenant-scoped RLS (DP-02).
ALTER TABLE engagement_notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE engagement_notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON engagement_notifications
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 010_create_notification_preferences.up.sql (engagement consolidation)
-- ==========================================================================
-- 010_create_notification_preferences.up.sql
-- NotificationPreferences — per-GCID per-tenant notification channel preferences.
-- Defaults applied in domain service when no row exists.
-- Tenant-scoped RLS: queries filtered by tenant_id.

CREATE TABLE notification_preferences (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid                  UUID        NOT NULL,  -- cross-context reference, no FK
    tenant_id             UUID        NOT NULL,  -- cross-context reference, no FK
    push_enabled          BOOLEAN     NOT NULL DEFAULT true,
    email_digest_enabled  BOOLEAN     NOT NULL DEFAULT true,
    streak_alerts         BOOLEAN     NOT NULL DEFAULT true,
    goal_alerts           BOOLEAN     NOT NULL DEFAULT true,
    daily_dose_reminders  BOOLEAN     NOT NULL DEFAULT true,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One preferences record per GCID per tenant.
CREATE UNIQUE INDEX uq_notification_preferences_gcid_tenant
    ON notification_preferences (gcid, tenant_id);

-- Tenant-scoped queries.
CREATE INDEX idx_notification_preferences_tenant_id
    ON notification_preferences (tenant_id);

-- Auto-update updated_at.
CREATE TRIGGER trg_notification_preferences_updated_at
    BEFORE UPDATE ON notification_preferences
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Tenant-scoped RLS (DP-02).
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_preferences
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 011_processed_events.up.sql (engagement consolidation)
-- ==========================================================================
-- Idempotency guard for event consumers.
-- Prevents duplicate processing of domain events (Pub/Sub at-least-once delivery).
CREATE TABLE IF NOT EXISTS processed_events (
    event_id     UUID         NOT NULL,
    handler_name VARCHAR(100) NOT NULL,
    processed_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    result_summary JSONB,
    PRIMARY KEY (event_id, handler_name)
);

CREATE INDEX idx_processed_events_handler ON processed_events (handler_name, processed_at);

COMMENT ON TABLE processed_events IS 'Idempotency guard — tracks which events have been processed by which handler';

-- ==========================================================================
-- Migration: 012_create_predicted_grades.up.sql (engagement consolidation)
-- ==========================================================================
-- 012_create_predicted_grades.up.sql
-- Phase 52.1.7: Predicted grade dashboard ML

CREATE TABLE IF NOT EXISTS predicted_grades (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid            UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    topic_id        UUID NOT NULL,
    topic_name      TEXT NOT NULL DEFAULT '',
    predicted_score DOUBLE PRECISION NOT NULL,
    confidence      DOUBLE PRECISION NOT NULL,
    model_version   TEXT NOT NULL DEFAULT '',
    predicted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Unique constraint: one prediction per learner per topic per tenant
CREATE UNIQUE INDEX idx_predicted_grades_unique ON predicted_grades (gcid, tenant_id, topic_id);
CREATE INDEX idx_predicted_grades_gcid ON predicted_grades (gcid, tenant_id);

-- RLS
ALTER TABLE predicted_grades ENABLE ROW LEVEL SECURITY;
ALTER TABLE predicted_grades FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON predicted_grades
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 013_create_data_pipelines.up.sql (engagement consolidation)
-- ==========================================================================
-- 53.19 — AI Data Flow Architecture (CHO-380)
-- Data pipeline configs and flow records

CREATE TABLE data_pipeline_configs (
    id               UUID PRIMARY KEY,
    tenant_id        UUID NOT NULL,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    source_type      TEXT NOT NULL,
    source_id        TEXT NOT NULL,
    destination_type TEXT NOT NULL,
    destination_id   TEXT NOT NULL,
    transform_rules  JSONB NOT NULL DEFAULT '{}',
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at       TIMESTAMPTZ
);

CREATE INDEX idx_data_pipeline_configs_tenant ON data_pipeline_configs(tenant_id);
CREATE INDEX idx_data_pipeline_configs_active ON data_pipeline_configs(tenant_id, is_active) WHERE is_active = TRUE AND deleted_at IS NULL;

ALTER TABLE data_pipeline_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE data_pipeline_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON data_pipeline_configs
  FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE TABLE data_flow_records (
    id                UUID PRIMARY KEY,
    pipeline_id       UUID NOT NULL REFERENCES data_pipeline_configs(id),
    tenant_id         UUID NOT NULL,
    records_processed INTEGER NOT NULL DEFAULT 0,
    records_errored   INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'running',
    error_message     TEXT,
    started_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_data_flow_records_pipeline ON data_flow_records(pipeline_id);
CREATE INDEX idx_data_flow_records_tenant ON data_flow_records(tenant_id);
CREATE INDEX idx_data_flow_records_status ON data_flow_records(tenant_id, status);

ALTER TABLE data_flow_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE data_flow_records FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON data_flow_records
  FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 014_create_ui_discovery_states.up.sql (engagement consolidation)
-- ==========================================================================
-- Migration 014: UIDiscoveryState (Fog-of-War per-learner TopicNode reveal status)
-- Supports: CHO-321, CHO-323, CHO-325

CREATE TYPE reveal_status AS ENUM ('hidden', 'revealed', 'conquered');

CREATE TABLE ui_discovery_states (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gcid                  UUID          NOT NULL,
    tenant_id             UUID          NOT NULL,
    topic_node_id         UUID          NOT NULL,
    reveal_status         reveal_status NOT NULL DEFAULT 'hidden',
    revealed_at           TIMESTAMPTZ,
    conquered_at          TIMESTAMPTZ,
    map_position_override JSONB,
    created_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT uq_discovery_state_gcid_tenant_topic
        UNIQUE (gcid, tenant_id, topic_node_id)
);

CREATE INDEX idx_discovery_states_gcid_tenant
    ON ui_discovery_states (gcid, tenant_id);

CREATE INDEX idx_discovery_states_tenant_topic
    ON ui_discovery_states (tenant_id, topic_node_id);

CREATE TRIGGER set_updated_at_ui_discovery_states
    BEFORE UPDATE ON ui_discovery_states
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS: tenant isolation + GCID-scoped
ALTER TABLE ui_discovery_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE ui_discovery_states FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON ui_discovery_states
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 015_create_star_accounts.up.sql (engagement consolidation)
-- ==========================================================================
-- Migration 015: StarAccount (XP Level Progression & Star Tier)
-- Supports: CHO-359

CREATE TYPE star_tier AS ENUM (
    'bronze_star',
    'silver_star',
    'gold_star',
    'platinum_star',
    'diamond_star'
);

CREATE TABLE star_accounts (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID        NOT NULL,
    gcid              UUID        NOT NULL,
    total_xp          BIGINT      NOT NULL DEFAULT 0,
    current_level     INT         NOT NULL DEFAULT 1,
    current_star_tier star_tier   NOT NULL DEFAULT 'bronze_star',
    level_achieved_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ,
    CONSTRAINT uq_star_accounts_tenant_gcid
        UNIQUE (tenant_id, gcid)
);

CREATE INDEX idx_star_accounts_tenant_gcid
    ON star_accounts (tenant_id, gcid)
    WHERE deleted_at IS NULL;

CREATE TRIGGER set_updated_at_star_accounts
    BEFORE UPDATE ON star_accounts
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS: tenant isolation
ALTER TABLE star_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE star_accounts FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON star_accounts
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);


COMMIT;

-- =============================================================================
-- chora-consumption : 0048_learner_retention.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : L4 Learner Journey — retention-loop durability (CHO-1702),
--                 no-debt/fail-loud directive 2026-06-10
-- Architecture  : internal/domain/familiar/retention_ports.go (SM2Store /
--                 StreakStore / XPStore) + internal/adapter/repo/pg/retention.go
--
-- Purpose:
--   The Maya retention loop (SM-2 spaced-repetition state, seen-topics,
--   daily streak, lifetime XP) previously lived ONLY in in-memory repos —
--   every pod restart wiped each learner's Ebbinghaus state, streak and XP,
--   so the daily-dose review slot could never survive a deploy. These four
--   tables make the loop durable + replica-consistent.
--
--   No soft-delete columns: these are mutable learner-state counters, not
--   aggregate roots — no domain operation deletes them. Account closure
--   reaches them via the per-domain PII_Closure_Map (gcid pseudonymisation /
--   CMEK crypto-shred), not row deletion.
--
-- RLS-enabled — composes with the multi-tenant-rls skill (every read/write
-- runs rls.ApplySession first).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- sm2_states — one row per (learner, atom): canonical SM-2 n / I / EF + the
-- review clock that drives the Ebbinghaus dose slot.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sm2_states (
    tenant_id        UUID             NOT NULL,
    learner_gcid     UUID             NOT NULL,
    atom_id          UUID             NOT NULL,
    repetitions      INT              NOT NULL DEFAULT 0,
    interval_days    INT              NOT NULL DEFAULT 0,
    easiness_factor  DOUBLE PRECISION NOT NULL DEFAULT 2.5,
    last_reviewed_at TIMESTAMPTZ,
    next_review_at   TIMESTAMPTZ,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, learner_gcid, atom_id)
);

CREATE INDEX IF NOT EXISTS idx_sm2_states_learner
    ON sm2_states (tenant_id, learner_gcid);

ALTER TABLE sm2_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE sm2_states FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON sm2_states;
CREATE POLICY tenant_isolation ON sm2_states
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- learner_seen_topics — topics the learner has interacted with (drives the
-- curiosity slot's adjacency heuristic + the recommender topic hint).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_seen_topics (
    tenant_id     UUID        NOT NULL,
    learner_gcid  UUID        NOT NULL,
    topic         TEXT        NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, learner_gcid, topic)
);

ALTER TABLE learner_seen_topics ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_seen_topics FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_seen_topics;
CREATE POLICY tenant_isolation ON learner_seen_topics
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- learner_streaks — per-learner daily activity streak (UTC-day semantics in
-- familiar.Streak.RecordActivity; the adapter persists the entity verbatim).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_streaks (
    tenant_id        UUID        NOT NULL,
    learner_gcid     UUID        NOT NULL,
    streak_count     INT         NOT NULL DEFAULT 0,
    last_activity_at TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, learner_gcid)
);

ALTER TABLE learner_streaks ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_streaks FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_streaks;
CREATE POLICY tenant_isolation ON learner_streaks
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- learner_xp — per-learner lifetime engagement XP (independent of any
-- Familiar instance's level).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_xp (
    tenant_id    UUID        NOT NULL,
    learner_gcid UUID        NOT NULL,
    xp           INT         NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, learner_gcid)
);

ALTER TABLE learner_xp ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_xp FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_xp;
CREATE POLICY tenant_isolation ON learner_xp
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- (re-runs lex-last on every migration job; ALTER DEFAULT PRIVILEGES grants
-- app_rw / app_ro on new tables automatically — no explicit GRANT here).

COMMIT;

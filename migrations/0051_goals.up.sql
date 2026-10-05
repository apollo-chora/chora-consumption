-- =============================================================================
-- chora-consumption : 0051_goals.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : KG goal-first redesign — learner-owned Goal aggregate
--                 (ADR-204 §2, amends ADR-203), 2026-06-29
-- Architecture  : docs/architecture/adrs/adr-204-knowledge-graph-goal-first-micro-effort-surface.md
--                 docs/architecture/adrs/adr-203-familiar-goal-and-verified-exp-anti-gaming.md
--                 internal/domain/goal/* (aggregate + Repository port)
--
-- Purpose:
--   The learner-owned Goal — the curious-first loop's destination, owned by the
--   LEARNER (not bound inside a Familiar). This makes the whole BASE loop (goal +
--   dose + KG + LearnerProfile) runnable with ZERO Familiars (ADR-204
--   Constraint-1). A Familiar OPTIONALLY attaches 1:1 via attached_familiar_id;
--   the bond persists here on the Goal.
--
--   kind ∈ {curiosity, cert, course, path, theme_mastery, edge}. chora_target_ref
--   is the verifiable Chora object (NULL only for an open `curiosity` goal); it is
--   an opaque cross-domain reference — no FK (ddd-enforcement #3). attached_familiar_id
--   references a Familiar in this same DB but is a cross-aggregate reference — no
--   FK either (ddd-enforcement #3). concept_set is the derived scope.
--
--   DEFERRED (NOT in this migration): the graduation state machine, goal.* events
--   / outbox, dose / forgetting-curve scoping (ADR-202), SplitCluster (ADR-204 §9).
--
-- Soft-delete only (`deleted_at`) per ddd-enforcement #5. RLS-enabled (every
-- read/write runs rls.ApplySession first). UUID default; UUIDv7 minted in the
-- domain layer (ddd-enforcement #7).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- goals — one row per learner Goal. Natural ownership is (tenant, learner); a
-- learner may hold several goals (one active per theme is an application-layer
-- concern, not a DB constraint, since kinds vary). The derived primary_lens
-- (ADR-204 §3) is computed at read time from active credential-kind rows — it is
-- NEVER stored (a stored mode-enum would be a hidden toggle).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS goals (
    id                   UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID         NOT NULL,
    learner_gcid         UUID         NOT NULL,
    kind                 VARCHAR(16)  NOT NULL
                                      CHECK (kind IN (
                                          'curiosity', 'cert', 'course',
                                          'path', 'theme_mastery', 'edge')),
    chora_target_ref     TEXT,                                    -- verifiable object; NULL for open curiosity; no FK (#3)
    concept_set          TEXT[]       NOT NULL DEFAULT '{}',      -- derived scope
    status               VARCHAR(16)  NOT NULL DEFAULT 'active'
                                      CHECK (status IN (
                                          'active', 'achieved',
                                          'maintenance', 'retired')),
    north_star_note      TEXT         NOT NULL DEFAULT '',        -- free-text, non-EXP (ADR-203 #3)
    attached_familiar_id UUID,                                    -- optional 1:1 bond; cross-aggregate, no FK (#3)
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ                              -- soft-delete (ddd-enforcement #5)
);

-- Hot path: the per-learner goal list (ListByLearner) + the derived-lens scan are
-- both per-learner over live rows.
CREATE INDEX IF NOT EXISTS idx_goals_learner
    ON goals (tenant_id, learner_gcid) WHERE deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- multi-tenant-rls skill: every read/write runs SET LOCAL chora.tenant_id
-- (rls.ApplySession) BEFORE the user query. Per-learner scoping is an explicit
-- learner_gcid predicate in every query (RLS isolates the tenant; the learner is
-- the validated session subject). Mirrors learner_profile (0050) / learner_weakness
-- (0046): ENABLE + FORCE + a single FOR ALL tenant_isolation policy.
-- -----------------------------------------------------------------------------
ALTER TABLE goals ENABLE ROW LEVEL SECURITY;
ALTER TABLE goals FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON goals;
CREATE POLICY tenant_isolation ON goals
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- (re-runs lex-last on every migration job; ALTER DEFAULT PRIVILEGES grants
-- app_rw / app_ro on new tables automatically — no explicit GRANT here).

COMMIT;

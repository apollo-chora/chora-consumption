-- =============================================================================
-- chora-consumption : 0071_familiar_rituals.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- Story : CHO-2016 — Grimoire Rituals v1 (ADR-219 D3/D4; spec
--         docs/FAMILIAR-SKILL-SPECS-2026-07-03.md §5). Learner-designed,
--         deterministic Skill pipelines: the designer aggregate
--         (familiar_rituals) + APPEND-ONLY revisions (familiar_ritual_revisions,
--         AtomRevision discipline) + STAMPED runs (familiar_ritual_runs,
--         ADR-197 substrate projected role-scoped per ADR-215).
--
-- ⚠ MIGRATION NUMBER 0071 chosen defensively: 0070 was the last on origin/main
--   AND the local worktree at authoring time (2026-07-07). A CONCURRENT WS1
--   effort also touches chora-consumption — if WS1 took 0071, RENUMBER this pair
--   (up+down) before apply/commit reconcile. Re-check pre-apply:
--     git ls-tree origin/main services/chora-consumption/migrations
--
-- Shape notes (conventions mirror 0067_ceremony_edge_scout_runs / 0060 headers):
--   - PKs default gen_random_uuid(); the AUTHORITATIVE id is a UUIDv7 minted
--     app-side (pg repos → domain.NewUUIDv7), the default is a safety net only.
--   - familiar_id / ritual_id cross-aggregate refs carry NO FK (ddd-enforcement
--     §3: refs are UUID without FK; the Ritual is its own aggregate root
--     referencing the Familiar). Same soft-delete-survival rationale as 0067.
--   - RLS ENABLE + FORCE with the standard tenant_isolation policy on all three;
--     per-learner scoping (runs) is an explicit owner_gcid predicate app-side.
--   - familiar_rituals: soft-delete via deleted_at (NEVER hard-delete); default
--     reads filter deleted_at IS NULL.
--   - familiar_ritual_revisions: APPEND-ONLY — no updated_at, no deleted_at, no
--     UPDATE/DELETE path in the adapter (AtomRevision discipline). UNIQUE
--     (ritual_id, revision_no) makes the append idempotent under replay.
--   - familiar_ritual_runs: an AUDIT + PRICING fact (decision stamp, mana) — NO
--     deleted_at (mirrors 0067's ledger carve-out; ddd-enforcement soft-delete
--     exception). Closure-saga treatment = pseudonymise owner_gcid like every
--     other gcid column; the row itself never deletes. The partial index on
--     status='running' powers the replicas=1 stale-running sweep (ADR-219 D4).
--
-- HARD INVARIANT: idempotent + revertable; GRANTs delegated to
-- 9999_grant_app_roles.sql (GRANT ... ON ALL TABLES IN SCHEMA covers these).
-- Do NOT apply manually — migrations auto-apply at deploy.
-- =============================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- familiar_rituals — the designer aggregate root
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_rituals (
    ritual_id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID        NOT NULL,
    familiar_id           UUID        NOT NULL,   -- opaque same-DB ref, no FK
    name                  TEXT        NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
    trigger               TEXT        NOT NULL
        CHECK (trigger IN ('manual','on_map_open','on_dose_completed','schedule','on_event')),
    trigger_config        JSONB       NOT NULL DEFAULT '{}',
    sink                  TEXT        NOT NULL
        CHECK (sink IN ('chat','memory_note','suggestion_inbox','question_bank','notification','calendar_artifact')),
    enabled               BOOLEAN     NOT NULL DEFAULT FALSE,
    published_price_units INTEGER     NOT NULL DEFAULT 0 CHECK (published_price_units >= 0),
    current_revision      INTEGER     NOT NULL DEFAULT 0 CHECK (current_revision >= 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ
);

COMMENT ON TABLE familiar_rituals IS
    'CHO-2016 Rituals v1 designer aggregate: learner-composed deterministic Skill pipelines, one closed sink, composed-flat published price. Soft-delete only.';

CREATE INDEX IF NOT EXISTS familiar_rituals_by_familiar
    ON familiar_rituals (tenant_id, familiar_id)
    WHERE deleted_at IS NULL;

ALTER TABLE familiar_rituals ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_rituals FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON familiar_rituals;
CREATE POLICY tenant_isolation ON familiar_rituals
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- ---------------------------------------------------------------------------
-- familiar_ritual_revisions — APPEND-ONLY step snapshots (AtomRevision)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_ritual_revisions (
    revision_id   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,   -- denormalised for the RLS policy
    ritual_id     UUID        NOT NULL,   -- opaque ref to familiar_rituals, no FK
    revision_no   INTEGER     NOT NULL CHECK (revision_no >= 1),
    steps         JSONB       NOT NULL,   -- [{skill_key, params}] × 1..8
    armor_verdict TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT familiar_ritual_revisions_no_unique UNIQUE (ritual_id, revision_no)
);

COMMENT ON TABLE familiar_ritual_revisions IS
    'CHO-2016 append-only Ritual revisions (AtomRevision discipline): no UPDATE/DELETE path. UNIQUE(ritual_id, revision_no) idempotent under replay.';

CREATE INDEX IF NOT EXISTS familiar_ritual_revisions_by_ritual
    ON familiar_ritual_revisions (tenant_id, ritual_id, revision_no DESC);

ALTER TABLE familiar_ritual_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_ritual_revisions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON familiar_ritual_revisions;
CREATE POLICY tenant_isolation ON familiar_ritual_revisions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- ---------------------------------------------------------------------------
-- familiar_ritual_runs — STAMPED runs (audit + pricing fact; no soft-delete)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_ritual_runs (
    run_id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    familiar_id    UUID        NOT NULL,
    owner_gcid     UUID        NOT NULL,
    ritual_id      UUID        NOT NULL,   -- opaque ref, no FK
    ritual_name    TEXT        NOT NULL DEFAULT '',   -- denormalised for the learner story
    revision_no    INTEGER     NOT NULL CHECK (revision_no >= 1),
    trigger_source TEXT        NOT NULL DEFAULT 'manual',
    status         TEXT        NOT NULL
        CHECK (status IN ('running','completed','failed','skipped_budget','blocked')),
    mana_charged   INTEGER     NOT NULL DEFAULT 0 CHECK (mana_charged >= 0),
    reservation_id TEXT        NOT NULL DEFAULT '',
    decision_stamp JSONB       NOT NULL DEFAULT '[]',  -- per-step ADR-197 stamps (O+ record)
    sink_ref       TEXT        NOT NULL DEFAULT '',
    error          TEXT        NOT NULL DEFAULT '',
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at   TIMESTAMPTZ
);

COMMENT ON TABLE familiar_ritual_runs IS
    'CHO-2016 stamped Ritual runs (ADR-197 substrate, ADR-215 role-scoped projection). Audit+pricing fact — never deleted; closure pseudonymises owner_gcid.';

-- Powers the replicas=1 stale-running sweep (ADR-219 D4): find orphaned runs.
CREATE INDEX IF NOT EXISTS familiar_ritual_runs_running
    ON familiar_ritual_runs (started_at)
    WHERE status = 'running';

-- Powers a learner's run history for a familiar (newest first).
CREATE INDEX IF NOT EXISTS familiar_ritual_runs_by_familiar
    ON familiar_ritual_runs (tenant_id, familiar_id, started_at DESC);

ALTER TABLE familiar_ritual_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_ritual_runs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON familiar_ritual_runs;
CREATE POLICY tenant_isolation ON familiar_ritual_runs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

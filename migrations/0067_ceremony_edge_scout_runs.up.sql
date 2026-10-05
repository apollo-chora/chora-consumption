-- =============================================================================
-- chora-consumption : 0067_ceremony_edge_scout_runs.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- Story : CHO-2040 — ceremony edge-scout (owner ruling R8-1, 2026-07-04):
--         the FIRST edge-scout run per (tenant, goal, learner) is FREE
--         (action code familiar_ceremony_edge_scout_first, mana 0); re-runs
--         pay the locked 25 (familiar_ceremony_edge_scout). This table IS the
--         scoping mechanism: no row ⇒ the runner stamps the zero-cost code on
--         the engine turn (still metered at the model-gateway — IMDA D3) and
--         records the row as part of the SAME successful flow; row exists ⇒
--         the unchanged paid path. Companion identity seed: chora-identity
--         migration 0032 (25 / 0 in mana_action_pricing).
--
-- Shape notes (conventions mirror 0060_concept_suggestions / 0066 headers):
--   - PK default gen_random_uuid(); the AUTHORITATIVE id is a UUIDv7 minted
--     app-side (pg.CeremonyEdgeScoutRunRepo → domain.NewUUIDv7), the default
--     is a safety net only (#7).
--   - goal_id is an opaque cross-aggregate UUID ref, NO FK (#3) — the Goal
--     aggregate lives in this DB but the ledger must survive goal soft-delete
--     (a re-created goal id is a NEW triple; a soft-deleted goal's freebie
--     stays burnt).
--   - UNIQUE(tenant_id, goal_id, learner_gcid) + the repo's ON CONFLICT DO
--     NOTHING absorb the concurrent first-run double-tap: the freebie burns
--     exactly once.
--   - NO deleted_at, deliberately: this is a PRICING LEDGER fact, not domain
--     content — deleting a row (soft or hard) would re-grant the owner-ruled
--     freebie (mirrors the 0031 idempotency_keys operational-table carve-out;
--     ddd-enforcement §Soft Deletes Exceptions). Closure-saga treatment is
--     pseudonymisation of learner_gcid like every other gcid column — the
--     row itself never deletes.
--   - RLS ENABLE + FORCE with the standard tenant_isolation policy (matches
--     concept_suggestions / goals); per-learner scoping is an explicit
--     learner_gcid predicate app-side on top.
--
-- HARD INVARIANT: idempotent + revertable; GRANTs delegated to
-- 9999_grant_app_roles.sql. Do NOT apply manually — migrations auto-apply at
-- deploy (deploy runbook: apply BEFORE rolling the CHO-2040 Unit B build; the
-- runner 500s loudly on a missing table rather than mispricing).
-- =============================================================================
BEGIN;

CREATE TABLE IF NOT EXISTS ceremony_edge_scout_runs (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,
    goal_id       UUID        NOT NULL,   -- opaque Goal ref, no FK (#3)
    learner_gcid  UUID        NOT NULL,
    first_run_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT ceremony_edge_scout_runs_one_freebie
        UNIQUE (tenant_id, goal_id, learner_gcid)
);

COMMENT ON TABLE ceremony_edge_scout_runs IS
    'R8-1 first-run ledger (CHO-2040): one row per (tenant, goal, learner) whose free ceremony edge-scout run is burnt; re-runs pay. Pricing fact — never deleted.';

ALTER TABLE ceremony_edge_scout_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE ceremony_edge_scout_runs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON ceremony_edge_scout_runs;
CREATE POLICY tenant_isolation ON ceremony_edge_scout_runs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

-- =============================================================================
-- chora-consumption : 0070_learner_dose_kg_prefs.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-224 Daily-Dose Composer v2 — per-KG dose preference
--                 (CHO-2045, sub-phase B1), 2026-07-06
-- Architecture  : docs/architecture/adrs/adr-224-daily-dose-composer-v2-subject-
--                   kg-balance-sampling-preferences.md (change #3)
--                 internal/domain/dose_pref/* (aggregate + Repository port)
--
-- Purpose:
--   Per-(learner, map/KG) include/exclude preference for the daily dose. The
--   ABSENCE of a row means the map is INCLUDED in dose sampling (default-
--   included); an explicit row records the learner's choice — most rows exist to
--   record an EXCLUSION. Excluding a map never stops weakness/retention tracking
--   for that map; it only removes the map's atoms from the daily-dose candidate
--   pool (the composer's pool filter consumes the excluded-map set).
--
-- Learner-sovereign (per-user KG, ADR-143): each row is scoped to one learner
--   (learner_gcid) within one tenant (tenant_id), never a per-tenant shared list.
--
-- Soft-delete only (`deleted_at`) per .claude/rules/ddd-enforcement.md §4 — the
-- upsert conflict target is a PARTIAL unique index over live rows, so a soft-
-- deleted preference never blocks a fresh one (mirrors the 0046 learner_weakness
-- partial-index-on-live-rows idiom). RLS-enabled — composes with the
-- multi-tenant-rls skill (every read/write runs rls.ApplySession first). UUID
-- default + UUIDv7 minted in the domain layer.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- learner_dose_kg_prefs — one live row per (tenant, learner, map).
--
-- map_id references a MapCluster / per-user KG map (chora_consumption-local, but
-- held as an opaque UUID without an FK per ddd-enforcement §3 cross-aggregate
-- refs). `included = true` re-includes a previously-excluded map.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_dose_kg_prefs (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL,
    learner_gcid  UUID         NOT NULL,
    map_id        UUID         NOT NULL,
    included      BOOLEAN      NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ                             -- soft-delete per ddd-enforcement §4
);

-- Upsert conflict target — one LIVE preference per (tenant, learner, map). A
-- PARTIAL unique index over live rows (WHERE deleted_at IS NULL) so a soft-
-- deleted row never blocks re-creating the preference; the adapter's
-- `INSERT ... ON CONFLICT (tenant_id, learner_gcid, map_id) WHERE deleted_at IS
-- NULL DO UPDATE` names this exact predicate to infer the arbiter. Its leftmost
-- (tenant_id, learner_gcid) prefix also serves the full-list read path.
CREATE UNIQUE INDEX IF NOT EXISTS idx_learner_dose_kg_prefs_live
    ON learner_dose_kg_prefs (tenant_id, learner_gcid, map_id) WHERE deleted_at IS NULL;

-- Hot path: the dose composer's ExcludedMapIDs lookup — the excluded live rows
-- for a learner. A targeted partial index (not a duplicate of the unique index)
-- keyed by (tenant_id, learner_gcid) over the excluded live subset only.
CREATE INDEX IF NOT EXISTS idx_learner_dose_kg_prefs_excluded
    ON learner_dose_kg_prefs (tenant_id, learner_gcid)
    WHERE deleted_at IS NULL AND included = false;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- multi-tenant-rls skill: every read/write runs SET LOCAL chora.tenant_id
-- (rls.ApplySession) BEFORE the user query. Per-learner scoping is an explicit
-- learner_gcid predicate in every query (RLS isolates the tenant; the learner is
-- the validated session subject). Mirrors 0046_learner_weakness.
-- -----------------------------------------------------------------------------
ALTER TABLE learner_dose_kg_prefs ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_dose_kg_prefs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_dose_kg_prefs;
CREATE POLICY tenant_isolation ON learner_dose_kg_prefs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- (re-runs lex-last on every migration job; ALTER DEFAULT PRIVILEGES grants
-- app_rw / app_ro on new tables automatically — no explicit GRANT here).

COMMIT;

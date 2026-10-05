-- =============================================================================
-- chora-consumption : 0039_kg_canvas_handlers.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption (ONLY — cross-DB queries forbidden)
-- Author        : E2E-BE-KG-CANVAS close (2026-05-16)
-- Architecture  : docs/architecture/adrs/adr-143-per-user-knowledge-graph-hexagonal-fog.md
--                 docs/m13/e2e-fe-coord-directive-2026-05-16.md row KG-CANVAS
--                 docs/m13/kg-fog-aplus-integration-backend-handoff-2026-05-13.md §6
--
-- Purpose:
--   Adds the kg_tenant_config table for endpoints #7 + #8 of the FE handoff
--   brief ("H+ tenant KG config GET/PATCH"). Previously the per-tenant
--   dials lived only in `clients.StaticKGConfigReader` (env-default for all
--   tenants); the canvas screens need real per-tenant persistence with
--   audit trail.
--
--   Two existing dials per ADR-143 §8:
--     - max_concurrent_kg_clusters_per_user  (int 1..10, default 3)
--     - kg_fog_invalidation_grace_seconds    (int 60..3600, default 300)
--
--   Plus audit fields:
--     - updated_at, updated_by_gcid, updated_by_display_name
--
--   No new schema for endpoints #1-#6 — they reuse the existing tables
--   from migration 0003 (kg_user_map_clusters / kg_user_explorations /
--   kg_hexagon_nodes / kg_exploration_trail / kg_junctions). See
--   internal/adapter/repo/inmem/user_kg.go for the in-memory analogue +
--   internal/adapter/http/kg_handler.go for the snake_case v1.0 routes
--   the new camelCase canvas handlers compose.
--
-- Soft-delete:
--   kg_tenant_config has no `deleted_at` column — tenant lifecycle is
--   owned by chora-tenancy; deleting a tenant cascades through the
--   federated closure saga (per ADR-141 + project_chora_concerns).
--   Crypto-shred of audit metadata via the closure saga is sufficient.
--
-- RLS:
--   Per multi-tenant-rls skill: tenant_id only (no user_gcid). H+ admin
--   role is the only writer; learner role is read-only (gated at handler
--   via role check, not at RLS — keeps the policy simple).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- kg_tenant_config — per-tenant KG dials (ADR-143 §8)
-- -----------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS kg_tenant_config (
    tenant_id                                UUID         PRIMARY KEY,
    max_concurrent_kg_clusters_per_user      INTEGER      NOT NULL DEFAULT 3
        CHECK (max_concurrent_kg_clusters_per_user BETWEEN 1 AND 10),
    kg_fog_invalidation_grace_seconds        INTEGER      NOT NULL DEFAULT 300
        CHECK (kg_fog_invalidation_grace_seconds BETWEEN 60 AND 3600),
    updated_at                               TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_by_gcid                          UUID         NULL,
    updated_by_display_name                  TEXT         NULL,
    created_at                               TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Audit-trail trigger — bumps updated_at automatically on UPDATE.
CREATE TRIGGER trg_kg_tenant_config_updated_at
    BEFORE UPDATE ON kg_tenant_config
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

-- RLS — tenant-id isolation (no user scoping; admin-only writes gated at handler).
ALTER TABLE kg_tenant_config ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_tenant_config
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

-- =============================================================================
-- Down migration (rollback) — informational; pre-M12 we rely on PITR.
-- =============================================================================
-- See 0039_kg_canvas_handlers.down.sql

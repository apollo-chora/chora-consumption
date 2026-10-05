-- =============================================================================
-- chora-consumption : 0059_familiar_map_binding.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-212 WS-3 (Learner-Sovereign Discovery Knowledge Graph, D5) — the durable
-- per-map/theme Familiar binding. Retires the legacy in-memory 1:1 "Companion"
-- (per-pod, non-persistent) as a source of truth: a map/theme's designated
-- Familiar is a PERSISTED familiar_instances row, and this table records the
-- (learner, map/theme) -> Familiar designation durably.
-- See internal/domain/familiar/map_binding.go.
--
-- Conventions mirror 0056_discovery_concept_graph.up.sql: soft-delete only
-- (deleted_at) #5; UUID default gen_random_uuid() but the authoritative id is a
-- UUIDv7 minted in the domain (#7); familiar_id is an opaque intra-domain ref
-- with NO FK (#3 — separate aggregate; soft-deleting an Instance must not be
-- blocked by referential integrity); RLS-enabled (ENABLE + FORCE). Grants
-- handled by 9999_grant_app_roles.sql (no explicit GRANT here).
-- =============================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- familiar_map_bindings — one designated Familiar per learner map/theme.
-- map_theme is the opaque learner-facing Discovery map/theme identifier
-- (distinct from familiar_instances.specialization).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_map_bindings (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL,
    learner_gcid  UUID         NOT NULL,
    map_theme     TEXT         NOT NULL,   -- opaque Discovery map/theme identifier
    familiar_id   UUID         NOT NULL,   -- designated familiar_instances row; cross-aggregate, no FK (#3)
    acquisition   VARCHAR(16)  NOT NULL DEFAULT 'hatched'
                               CHECK (acquisition IN (
                                   'hatched',       -- full egg/hatch ceremony (Stripe-gated in prod)
                                   'dev_hatched')), -- free/dev hatch path (realistic FE test data, D5)
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ                        -- soft-delete (#5)
);

CREATE INDEX IF NOT EXISTS idx_familiar_map_bindings_learner
    ON familiar_map_bindings (tenant_id, learner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_familiar_map_bindings_familiar
    ON familiar_map_bindings (tenant_id, familiar_id) WHERE deleted_at IS NULL;

-- ADR-212 D5: exactly ONE live designated Familiar per (tenant, learner,
-- map/theme). Partial so a soft-deleted binding doesn't block a re-designation.
CREATE UNIQUE INDEX IF NOT EXISTS uq_familiar_map_bindings_live_theme
    ON familiar_map_bindings (tenant_id, learner_gcid, map_theme) WHERE deleted_at IS NULL;

ALTER TABLE familiar_map_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_map_bindings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON familiar_map_bindings;
CREATE POLICY tenant_isolation ON familiar_map_bindings
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

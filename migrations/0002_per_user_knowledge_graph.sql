-- =============================================================================
-- chora_consumption migration 0002 — Per-User Knowledge Graph (Hexagonal Fog)
--
-- Source-of-truth: docs/architecture/adrs/adr-143-per-user-knowledge-graph-hexagonal-fog.md
--
-- This migration:
--   1. Creates 5 new enums and 5 new tables for the per-user hexagonal
--      exploration fog feature (MapCluster + Exploration + HexagonNode +
--      TrailHop + atom_semantic_edges).
--   2. Imports atom_semantic_edges seed data from chora_creation.knowledge_graph_edges
--      (one-shot copy via admin tooling — NOT runtime cross-DB query).
--   3. Wires RLS policies (dual tenant + user_gcid scoping where applicable).
--   4. Wires append-only trigger for kg_exploration_trail (mirroring
--      enforce_atom_revisions_append_only at chora-creation/migrations/0001:109-122).
--   5. Wires updated_at triggers for the OCC-tracked tables.
--
-- Big-bang migration is acceptable pre-M12 (zero live users). Rollback path:
-- 7d Cloud SQL Enterprise Plus PITR per database-postgresql skill.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Helpers — updated_at trigger function (mirror of creation_set_updated_at())
-- -----------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION consumption_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    NEW.version   = OLD.version + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- Helpers — append-only trigger function (mirror of
--           enforce_atom_revisions_append_only() at chora_creation 0001:109)
-- -----------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION enforce_kg_trail_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'kg_exploration_trail is append-only (ADR-143): % rejected', TG_OP;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------

CREATE TYPE kg_atom_edge_type AS ENUM (
    'prerequisite_of',
    'extends',
    'analogy_of',
    'contrasts_with',
    'applied_in'
);

CREATE TYPE kg_cluster_status AS ENUM (
    'active',
    'archived',
    'merged_into'
);

CREATE TYPE kg_exploration_status AS ENUM (
    'active',
    'archived'
);

CREATE TYPE kg_fog_invalidation_reason AS ENUM (
    'atom_published',
    'atom_revision_updated',
    'user_retention_shifted',
    'manual_admin',
    'never_generated'
);

CREATE TYPE kg_neighbor_relation AS ENUM (
    'prerequisite_of',
    'extends',
    'analogy_of',
    'contrasts_with',
    'applied_in',
    'curiosity_jump'
);

-- -----------------------------------------------------------------------------
-- atom_semantic_edges — RELOCATED from chora_creation, RENAMED from
-- knowledge_graph_edges (vestigial gcid column dropped). Authoring-time
-- atom-to-atom relationships — tenant-scoped, NOT user-scoped.
-- -----------------------------------------------------------------------------

CREATE TABLE atom_semantic_edges (
    edge_id          UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID                 NOT NULL,
    source_atom_id   UUID                 NOT NULL,    -- cross-DB ref to chora_creation atom
    target_atom_id   UUID                 NOT NULL,    -- cross-DB ref to chora_creation atom
    edge_type        kg_atom_edge_type    NOT NULL,
    weight           REAL                 NOT NULL DEFAULT 0.5 CHECK (weight >= 0 AND weight <= 1),
    version          INTEGER              NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ          NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ          NULL,
    CHECK (source_atom_id <> target_atom_id),
    UNIQUE (tenant_id, source_atom_id, target_atom_id, edge_type)
);

CREATE INDEX idx_ase_source ON atom_semantic_edges (tenant_id, source_atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_ase_target ON atom_semantic_edges (tenant_id, target_atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_ase_type   ON atom_semantic_edges (edge_type)                  WHERE deleted_at IS NULL;

CREATE TRIGGER trg_ase_updated_at
    BEFORE UPDATE ON atom_semantic_edges
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE atom_semantic_edges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atom_semantic_edges
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- kg_user_map_clusters — disconnected map abstraction (aggregate root for Join)
-- -----------------------------------------------------------------------------

CREATE TABLE kg_user_map_clusters (
    cluster_id              UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),  -- UUIDv7 in app code
    tenant_id               UUID                 NOT NULL,
    user_gcid               UUID                 NOT NULL,
    display_name            VARCHAR(128)         NOT NULL,
    seed_topic              TEXT                 NOT NULL,
    seed_atom_id            UUID                 NOT NULL,    -- cross-DB ref to chora_creation atom
    status                  kg_cluster_status    NOT NULL DEFAULT 'active',
    merged_into_cluster_id  UUID                 NULL,
    merged_via_atom_id      UUID                 NULL,
    node_count              INTEGER              NOT NULL DEFAULT 1,
    version                 INTEGER              NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ          NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ          NOT NULL DEFAULT now(),
    deleted_at              TIMESTAMPTZ          NULL,
    CHECK ((status = 'merged_into') = (merged_into_cluster_id IS NOT NULL AND merged_via_atom_id IS NOT NULL))
);

CREATE INDEX idx_kg_clusters_user_active ON kg_user_map_clusters (tenant_id, user_gcid)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE INDEX idx_kg_clusters_merged ON kg_user_map_clusters (tenant_id, merged_into_cluster_id)
    WHERE merged_into_cluster_id IS NOT NULL;

CREATE TRIGGER trg_kg_clusters_updated_at
    BEFORE UPDATE ON kg_user_map_clusters
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE kg_user_map_clusters ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_user_map_clusters
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON kg_user_map_clusters
    FOR ALL USING (
        user_gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- kg_user_explorations — seed-rooted journey within a cluster
-- -----------------------------------------------------------------------------

CREATE TABLE kg_user_explorations (
    exploration_id           UUID                     PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id               UUID                     NOT NULL REFERENCES kg_user_map_clusters(cluster_id) ON DELETE CASCADE,
    tenant_id                UUID                     NOT NULL,
    user_gcid                UUID                     NOT NULL,
    started_at_atom_id       UUID                     NOT NULL,
    current_focal_atom_id    UUID                     NOT NULL,
    status                   kg_exploration_status    NOT NULL DEFAULT 'active',
    version                  INTEGER                  NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ              NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ              NOT NULL DEFAULT now(),
    deleted_at               TIMESTAMPTZ              NULL
);

CREATE INDEX idx_kg_explorations_cluster ON kg_user_explorations (cluster_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_kg_explorations_user_active ON kg_user_explorations (tenant_id, user_gcid)
    WHERE status = 'active' AND deleted_at IS NULL;

CREATE TRIGGER trg_kg_explorations_updated_at
    BEFORE UPDATE ON kg_user_explorations
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE kg_user_explorations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_user_explorations
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON kg_user_explorations
    FOR ALL USING (
        user_gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- kg_hexagon_nodes — persisted fog cache (focal + 6 neighbors as JSONB)
-- -----------------------------------------------------------------------------

CREATE TABLE kg_hexagon_nodes (
    hex_node_id            UUID                          PRIMARY KEY DEFAULT gen_random_uuid(),
    exploration_id         UUID                          NOT NULL REFERENCES kg_user_explorations(exploration_id) ON DELETE CASCADE,
    cluster_id             UUID                          NOT NULL,    -- denormalised
    tenant_id              UUID                          NOT NULL,    -- denormalised RLS
    user_gcid              UUID                          NOT NULL,    -- denormalised RLS
    focal_atom_id          UUID                          NOT NULL,
    neighbors_json         JSONB                         NOT NULL,
    generated_by_run_id    UUID                          NOT NULL,
    generated_by_model_id  TEXT                          NOT NULL,
    generated_at           TIMESTAMPTZ                   NOT NULL DEFAULT now(),
    invalidated_at         TIMESTAMPTZ                   NULL,
    invalidation_reason    kg_fog_invalidation_reason    NULL,
    version                INTEGER                       NOT NULL DEFAULT 0,
    deleted_at             TIMESTAMPTZ                   NULL,
    CHECK (jsonb_typeof(neighbors_json) = 'array' AND jsonb_array_length(neighbors_json) = 6)
);

-- O(log n) cache lookup
CREATE UNIQUE INDEX idx_hex_focal ON kg_hexagon_nodes (exploration_id, focal_atom_id) WHERE deleted_at IS NULL;

-- Junction-detection key — answer "is this atom_id a focal in another cluster of this user?"
CREATE INDEX idx_hex_user_focals ON kg_hexagon_nodes (tenant_id, user_gcid, focal_atom_id) WHERE deleted_at IS NULL;

-- Sweeper — find invalidated rows
CREATE INDEX idx_hex_invalidated ON kg_hexagon_nodes (invalidated_at) WHERE invalidated_at IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE kg_hexagon_nodes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_hexagon_nodes
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON kg_hexagon_nodes
    FOR ALL USING (
        user_gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- NOTE: kg_hexagon_nodes does NOT use consumption_set_updated_at() — its
-- "version" semantic is driven explicitly by the domain (cache vs invalidate
-- vs regen are different lifecycle states than a generic UPDATE).

-- -----------------------------------------------------------------------------
-- kg_exploration_trail — append-only path history
-- -----------------------------------------------------------------------------

CREATE TABLE kg_exploration_trail (
    trail_id              UUID                     PRIMARY KEY DEFAULT gen_random_uuid(),
    exploration_id        UUID                     NOT NULL REFERENCES kg_user_explorations(exploration_id) ON DELETE CASCADE,
    cluster_id            UUID                     NOT NULL,    -- denormalised
    tenant_id             UUID                     NOT NULL,    -- denormalised RLS
    user_gcid             UUID                     NOT NULL,    -- denormalised RLS
    from_focal_atom_id    UUID                     NOT NULL,
    to_focal_atom_id      UUID                     NOT NULL,
    relation              kg_neighbor_relation     NOT NULL,
    step_index            INTEGER                  NOT NULL,
    traversed_at          TIMESTAMPTZ              NOT NULL DEFAULT now(),
    UNIQUE (exploration_id, step_index),
    CHECK (from_focal_atom_id <> to_focal_atom_id)
);

CREATE INDEX idx_trail_exploration_step ON kg_exploration_trail (exploration_id, step_index DESC);

CREATE TRIGGER trg_trail_no_update
    BEFORE UPDATE ON kg_exploration_trail
    FOR EACH ROW EXECUTE FUNCTION enforce_kg_trail_append_only();

CREATE TRIGGER trg_trail_no_delete
    BEFORE DELETE ON kg_exploration_trail
    FOR EACH ROW EXECUTE FUNCTION enforce_kg_trail_append_only();

ALTER TABLE kg_exploration_trail ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_exploration_trail
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON kg_exploration_trail
    FOR ALL USING (
        user_gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- atom_semantic_edges — one-shot data import from chora_creation
-- -----------------------------------------------------------------------------
--
-- Pre-migration data audit MUST verify zero rows depend on the vestigial
-- `gcid` column (expected: zero — current users absent pre-M12). The import
-- below drops the column on read.
--
-- This is admin tooling, NOT a runtime cross-DB query. Run via:
--
--   pg_dump --table=knowledge_graph_edges chora_creation \
--     | sed 's/knowledge_graph_edges/atom_semantic_edges/g' \
--     | sed 's/knowledge_edge_type/kg_atom_edge_type/g' \
--     | <strip-gcid-column-with-awk> \
--     | psql chora_consumption
--
-- (Or an equivalent ETL via Cloud SQL Enterprise Plus admin tooling.)
--
-- Post-import verification:
--   SELECT count(*) FROM atom_semantic_edges;  -- expect: imported row count
--   SELECT count(*) FROM atom_semantic_edges WHERE deleted_at IS NOT NULL;
--   SELECT count(DISTINCT tenant_id) FROM atom_semantic_edges;
--
-- Once verified, run sibling migration:
-- services/chora-creation/migrations/0002_drop_knowledge_graph_edges.sql

COMMIT;

-- =============================================================================
-- Down migration (rollback) — informational only; pre-M12 we lean on PITR
-- =============================================================================
-- BEGIN;
--   DROP TABLE IF EXISTS kg_exploration_trail CASCADE;
--   DROP TABLE IF EXISTS kg_hexagon_nodes CASCADE;
--   DROP TABLE IF EXISTS kg_user_explorations CASCADE;
--   DROP TABLE IF EXISTS kg_user_map_clusters CASCADE;
--   DROP TABLE IF EXISTS atom_semantic_edges CASCADE;
--   DROP TYPE IF EXISTS kg_neighbor_relation;
--   DROP TYPE IF EXISTS kg_fog_invalidation_reason;
--   DROP TYPE IF EXISTS kg_exploration_status;
--   DROP TYPE IF EXISTS kg_cluster_status;
--   DROP TYPE IF EXISTS kg_atom_edge_type;
--   DROP FUNCTION IF EXISTS enforce_kg_trail_append_only();
--   DROP FUNCTION IF EXISTS consumption_set_updated_at();
-- COMMIT;

-- =============================================================================
-- chora_consumption migration 0003 — Per-User Knowledge Graph (S5.2)
--
-- Source-of-truth: docs/architecture/adrs/adr-143-per-user-knowledge-graph-hexagonal-fog.md
--
-- Continues 0002 (which created MapCluster + Exploration + HexagonNode +
-- TrailHop + atom_semantic_edges). This migration adds:
--
--   1. kg_junctions — persistent record of detected cluster overlaps
--      with user-consented accept/reject lifecycle (status: pending |
--      accepted | rejected). Distinct from JunctionOpportunity (which
--      is the transient per-fog-call surface).
--   2. Optional view kg_active_clusters_per_user for the H+ admin
--      "users at cap" stat panel (cap is per-tenant configurable).
--
-- Cross-DB queries forbidden (per ddd-enforcement). All RLS policies
-- mirror the chora.tenant_id + chora.user_gcid pattern set by
-- libs/chora-go-common/rls/rls.go.
--
-- Per ADR-143 §6 the same (cluster_a, cluster_b) pair pre-existing as
-- pending is idempotent at the application layer — DB unique index
-- below enforces this for ACCEPTANCE only (rejected pairs CAN
-- re-detect, e.g. after a refresh of the user's exploration).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------

CREATE TYPE kg_junction_status AS ENUM (
    'pending',
    'accepted',
    'rejected'
);

-- -----------------------------------------------------------------------------
-- kg_junctions — persistent record of user-driven cluster join opportunities
-- -----------------------------------------------------------------------------

CREATE TABLE kg_junctions (
    junction_id              UUID                  PRIMARY KEY DEFAULT gen_random_uuid(),  -- UUIDv7 in app code
    tenant_id                UUID                  NOT NULL,
    user_gcid                UUID                  NOT NULL,
    cluster_a_id             UUID                  NOT NULL REFERENCES kg_user_map_clusters(cluster_id) ON DELETE CASCADE,
    cluster_b_id             UUID                  NOT NULL REFERENCES kg_user_map_clusters(cluster_id) ON DELETE CASCADE,
    overlap_atom_ids         UUID[]                NOT NULL,
    status                   kg_junction_status    NOT NULL DEFAULT 'pending',
    resolved_via_atom_id     UUID                  NULL,
    detected_at              TIMESTAMPTZ           NOT NULL DEFAULT now(),
    resolved_at              TIMESTAMPTZ           NULL,
    version                  INTEGER               NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ           NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ           NOT NULL DEFAULT now(),
    deleted_at               TIMESTAMPTZ           NULL,
    CHECK (cluster_a_id <> cluster_b_id),
    CHECK (cardinality(overlap_atom_ids) > 0),
    -- A pending junction must have a NULL resolved_at; resolved must have non-NULL.
    CHECK ((status = 'pending') = (resolved_at IS NULL)),
    -- Accepted junctions must specify the via-atom; rejected must not.
    CHECK (
        (status <> 'accepted') OR (resolved_via_atom_id IS NOT NULL)
    )
);

-- Index for the UI's notification badge: pending junctions per user.
CREATE INDEX idx_kg_junctions_user_pending
    ON kg_junctions (tenant_id, user_gcid)
    WHERE status = 'pending' AND deleted_at IS NULL;

-- Idempotency: only one PENDING junction per (tenant, user, sorted_pair)
-- at a time. Once accepted/rejected, re-detection on the same pair is
-- allowed (e.g. after the user creates a third bridging cluster).
CREATE UNIQUE INDEX idx_kg_junctions_pending_pair
    ON kg_junctions (
        tenant_id,
        user_gcid,
        LEAST(cluster_a_id, cluster_b_id),
        GREATEST(cluster_a_id, cluster_b_id)
    )
    WHERE status = 'pending' AND deleted_at IS NULL;

-- Provenance traversal — find junctions that touch a cluster.
CREATE INDEX idx_kg_junctions_cluster_a
    ON kg_junctions (cluster_a_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_kg_junctions_cluster_b
    ON kg_junctions (cluster_b_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_kg_junctions_updated_at
    BEFORE UPDATE ON kg_junctions
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE kg_junctions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kg_junctions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);
CREATE POLICY user_isolation ON kg_junctions
    FOR ALL USING (
        user_gcid = current_setting('chora.user_gcid', true)::uuid
        OR current_setting('chora.role', true) = 'admin'
    );

-- -----------------------------------------------------------------------------
-- View — kg_active_clusters_per_user (H+ admin "users at cap" stat panel)
-- -----------------------------------------------------------------------------

CREATE VIEW kg_active_clusters_per_user AS
    SELECT
        tenant_id,
        user_gcid,
        COUNT(*)::INTEGER AS active_cluster_count
    FROM kg_user_map_clusters
    WHERE status = 'active' AND deleted_at IS NULL
    GROUP BY tenant_id, user_gcid;

COMMIT;

-- =============================================================================
-- Down migration (rollback) — informational only; pre-M12 we lean on PITR
-- =============================================================================
-- BEGIN;
--   DROP VIEW IF EXISTS kg_active_clusters_per_user;
--   DROP TABLE IF EXISTS kg_junctions CASCADE;
--   DROP TYPE IF EXISTS kg_junction_status;
-- COMMIT;

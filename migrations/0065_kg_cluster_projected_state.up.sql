-- =============================================================================
-- chora-consumption : 0065_kg_cluster_projected_state.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-223 (MapCluster -> Goal projection, fog retirement). Adds the terminal
-- `projected` lifecycle state to the fog MapCluster aggregate + a
-- projected_into_goal_id anchor recording the sovereign Goal it became.
-- Amends kg_user_map_clusters (migrations/0002_per_user_knowledge_graph.sql).
--   projected      — terminal status beside archived / merged_into; the fog
--                    cluster is retired (never deleted) once re-projected.
--   projected_into_goal_id — the ADR-214 Goal (UUID, cross-aggregate ref, no FK)
--                    minted from this cluster. Set iff status = 'projected'
--                    (invariant enforced by the MapCluster aggregate, ADR-223 D1).
-- Safe in a transaction on PostgreSQL 12+: the new enum value is added but
-- NOT used within this migration (the column is UUID, not the enum), so no
-- "unsafe use of new value" error.
-- =============================================================================

ALTER TYPE kg_cluster_status ADD VALUE IF NOT EXISTS 'projected';

ALTER TABLE kg_user_map_clusters
    ADD COLUMN IF NOT EXISTS projected_into_goal_id UUID;

COMMENT ON COLUMN kg_user_map_clusters.projected_into_goal_id IS
    'ADR-223: the sovereign Goal this fog cluster was re-projected into; set iff status = projected. Cross-aggregate ref (no FK).';

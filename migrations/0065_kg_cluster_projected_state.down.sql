-- =============================================================================
-- chora-consumption : 0065_kg_cluster_projected_state.down.sql
-- Reverts ADR-223 projected_into_goal_id column.
-- NOTE: PostgreSQL cannot drop a single enum value without recreating the type;
-- the 'projected' value on kg_cluster_status is intentionally LEFT in place on
-- down (harmless — no rows reference it once the column is gone). Dropping the
-- column is the meaningful revert.
-- =============================================================================

ALTER TABLE kg_user_map_clusters
    DROP COLUMN IF EXISTS projected_into_goal_id;

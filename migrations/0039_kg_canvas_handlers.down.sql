-- =============================================================================
-- chora-consumption : 0039_kg_canvas_handlers.down.sql
--
-- Rollback for 0039_kg_canvas_handlers.up.sql. Pre-M12 we lean on PITR;
-- this file is informational only.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS kg_tenant_config;

COMMIT;

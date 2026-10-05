-- =============================================================================
-- chora-consumption : 0105_growth_edge_outputs.down.sql
--
-- Reverses 0105. Dropping the table discards projected artifacts, but they are
-- reproducible: the producing events are replayable from
-- chora.consumption.weakness.outputs_generated.v1 and the rows are a pure
-- projection, never a system of record.
-- =============================================================================

BEGIN;

DROP POLICY IF EXISTS tenant_isolation ON growth_edge_outputs;
DROP INDEX IF EXISTS growth_edge_outputs_learner_upload_idx;
DROP INDEX IF EXISTS growth_edge_outputs_upload_kind_uniq;
DROP TABLE IF EXISTS growth_edge_outputs;

COMMIT;

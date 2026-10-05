-- =============================================================================
-- chora-consumption : 0069_proofing_test_edge_signature.down.sql
-- Reverses 0069 — drops the in-flight signature guard + column.
-- =============================================================================
BEGIN;

DROP INDEX IF EXISTS uq_proofing_tests_inflight_signature;

ALTER TABLE proofing_tests
    DROP COLUMN IF EXISTS edge_signature;

COMMIT;

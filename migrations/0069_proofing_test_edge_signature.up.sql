-- =============================================================================
-- chora-consumption : 0069_proofing_test_edge_signature.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- CHO-2040 — concurrent-double-submit backstop for the Virgin Proofing Test
-- composed runner. The handler read-probes for an in-flight (requested|
-- composing) proofing test with the same goal + edge-title signature before
-- reserving mana; this partial-unique index closes the truly-concurrent race
-- where two requests both pass the probe before either commits (else the
-- learner double-charges + gets duplicate tests). edge_signature is the
-- derived idempotency key (goal id + sorted normalized deduped edge titles)
-- written by the pg adapter on INSERT. Additive + idempotent (IF NOT EXISTS);
-- RLS is inherited from 0068 (no policy change).
-- =============================================================================
BEGIN;

ALTER TABLE proofing_tests
    ADD COLUMN IF NOT EXISTS edge_signature TEXT NOT NULL DEFAULT '';

-- At most ONE in-flight (requested|composing) proofing test per
-- (tenant, learner, goal, edge signature). Terminal (ready|failed) and
-- soft-deleted rows are excluded, so a learner can always regenerate. Empty
-- signatures (only legacy rows, if any) are excluded — the runner always
-- writes a real one.
CREATE UNIQUE INDEX IF NOT EXISTS uq_proofing_tests_inflight_signature
    ON proofing_tests (tenant_id, learner_gcid, goal_id, edge_signature)
    WHERE deleted_at IS NULL
      AND status IN ('requested', 'composing')
      AND edge_signature <> '';

COMMIT;

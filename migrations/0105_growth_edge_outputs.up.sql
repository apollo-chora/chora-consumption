-- =============================================================================
-- chora-consumption : 0105_growth_edge_outputs.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-2348: ADR-205 WS-7 generated-output delivery
-- Date          : 2026-07-23
--
-- Purpose:
--   Land the metered artifacts the weakness-analyser crew generates (study_aids
--   prose + a critic-gated practice_test) so they reach the learner. Until now
--   the crew generated them, charged the learner's mana, and discarded the
--   result: _run_detached_generation awaited the generator and dropped it, and
--   the two kinds appeared nowhere outside the orchestrator. The learner paid
--   and received nothing.
--
--   Fed by chora.consumption.weakness.outputs_generated.v1 (emitted by
--   chora-ai-kernel-orchestrator from inside its DETACHED generation task, so
--   the HITL resume still returns without waiting on the LLM calls). Read by
--   the A+ Growth-Edge surface.
--
-- -----------------------------------------------------------------------------
-- CONTENT IS JSONB, NOT PER-KIND COLUMNS
-- -----------------------------------------------------------------------------
-- study_aids carries prose (a JSON string); practice_test carries a composed
-- object whose internal shape is still settling. One JSONB column keeps both
-- kinds in one table and one read path, and keeps an artifact gaining a field
-- from becoming a migration. `kind` is the discriminator the A+ surface renders
-- on. Per the owner decision of 2026-07-23 both kinds are READ-ONLY artifacts:
-- a practice_test is displayed, never attempted, so nothing here couples to the
-- ADR-246 atomic_sessions / AtomAttempt contracts.
--
-- -----------------------------------------------------------------------------
-- IDEMPOTENCY
-- -----------------------------------------------------------------------------
-- Pub/Sub is at-least-once, so redelivery must not duplicate a learner's
-- artifacts. The partial UNIQUE on (tenant_id, upload_id, kind) WHERE
-- deleted_at IS NULL lets the projection use ON CONFLICT DO NOTHING: one
-- artifact per kind per upload, and a soft-deleted row does not block a
-- regenerate. This is the projection-side half of the outbox idempotency_key
-- (weakness.outputs_generated.{upload_id}) the producer already carries.
--
-- source_event_id records WHICH delivery won, so a duplicate is diagnosable
-- rather than merely absent.
--
-- -----------------------------------------------------------------------------
-- ROW LEVEL SECURITY
-- -----------------------------------------------------------------------------
-- ENABLE + FORCE + tenant_isolation on chora.tenant_id, matching the sibling
-- weakness tables (0046 / 0047). Learner scoping is a query predicate on
-- learner_gcid, as in those tables: the tenant GUC is the isolation boundary,
-- the learner id is the selector.
--
-- ⚠ NEW TABLE ⇒ 9999_grant_app_roles.sql MUST RUN AFTER THIS.
-- 9999 grants at TABLE level and runs LAST. A targeted apply that skips it
-- leaves chora_consumption_app_rw without privileges on this table and every
-- read/write 42501s at runtime while the migration itself reports success.
--
-- Idempotent (IF NOT EXISTS throughout).
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS growth_edge_outputs (
    output_id       UUID         PRIMARY KEY,
    tenant_id       UUID         NOT NULL,
    learner_gcid    UUID         NOT NULL,
    upload_id       UUID         NOT NULL,          -- weakness_doc_uploads.upload_id
    kind            VARCHAR(24)  NOT NULL
                                 CHECK (kind IN ('study_aids', 'practice_test')),
    content         JSONB        NOT NULL,          -- prose (JSON string) or composed object
    metered         BOOLEAN      NOT NULL DEFAULT TRUE,
    source_event_id UUID,                           -- which delivery produced this row
    generated_at    TIMESTAMPTZ  NOT NULL,          -- when the crew finished generating
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ                     -- soft-delete per ddd-enforcement §5
);

-- One artifact per kind per upload; a soft-deleted row frees the slot.
CREATE UNIQUE INDEX IF NOT EXISTS growth_edge_outputs_upload_kind_uniq
    ON growth_edge_outputs (tenant_id, upload_id, kind)
    WHERE deleted_at IS NULL;

-- The learner read path: "my artifacts for this upload".
CREATE INDEX IF NOT EXISTS growth_edge_outputs_learner_upload_idx
    ON growth_edge_outputs (tenant_id, learner_gcid, upload_id)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE growth_edge_outputs IS
    'ADR-205 WS-7 generated learner artifacts (study_aids / practice_test) '
    'projected from chora.consumption.weakness.outputs_generated.v1. Read-only '
    'on the A+ Growth-Edge surface per the 2026-07-23 owner decision. CHO-2348.';

COMMENT ON COLUMN growth_edge_outputs.content IS
    'The artifact as JSONB: a JSON string for study_aids prose, a composed '
    'object for practice_test. One column so a new artifact field is not a '
    'migration; `kind` discriminates for rendering.';

COMMENT ON COLUMN growth_edge_outputs.metered IS
    'True when the learner spent mana on this kind (ADR-178 action codes). '
    'Carried so the surface can be honest about what was paid for.';

ALTER TABLE growth_edge_outputs ENABLE ROW LEVEL SECURITY;
ALTER TABLE growth_edge_outputs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON growth_edge_outputs;
CREATE POLICY tenant_isolation ON growth_edge_outputs
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

-- =============================================================================
-- chora-consumption : 0068_proofing_tests.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- CHO-2040 (owner rulings R8-6 + R8-7) — the Virgin Proofing Test composed
-- runner's aggregate: one learner-triggered (via their Familiar) request to
-- generate a fresh MCQ+OE assessment targeting the growth edges ticked at the
-- goal-binding ceremony. Consumption-owned + learner-takeable (NOT delivery's
-- instructor TestSet — cross-DB forbidden; the ai_assist batch is referenced
-- by opaque UUID only, #3). See internal/domain/proofingtest/proofingtest.go.
--
-- Conventions mirror 0060_concept_suggestions.up.sql: soft-delete only
-- (deleted_at, #5); the authoritative id is a UUIDv7 minted in the domain
-- (#7; gen_random_uuid() default is the safety net); goal/familiar/assist
-- references are opaque UUIDs with NO FK (#3); RLS ENABLE + FORCE with the
-- tenant_isolation policy shape. Grants via 9999_grant_app_roles.sql.
-- =============================================================================
BEGIN;

CREATE TABLE IF NOT EXISTS proofing_tests (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID         NOT NULL,
    learner_gcid    UUID         NOT NULL,
    goal_id         UUID         NOT NULL,   -- opaque ref → goals (no FK, #3)
    familiar_id     UUID         NOT NULL,   -- opaque ref → familiar_instances (no FK)
    assist_id       UUID         NOT NULL,   -- the chora.creation.ai_assist batch request ref

    status          VARCHAR(16)  NOT NULL DEFAULT 'requested'
                                 CHECK (status IN ('requested', 'composing', 'ready', 'failed')),

    -- The ticked ceremony learning-edges the set targets:
    -- [{concept_id, key, title, intent}] (R8-7: key = the qgen
    -- target_growth_edges concept key; the runner refuses un-keyed edges).
    target_edges    JSONB        NOT NULL DEFAULT '[]'::jsonb,

    -- Future cross-aggregate test-set reference (the LIVE completed.v1 embeds
    -- the composed set inline instead — stored below — so this stays NULL).
    testset_ref     TEXT,
    -- The composed batch payload {"candidates":[...],"proposed_test_set":{...}}
    -- stored verbatim on ready; the learner take-surface renders from it.
    testset_payload JSONB,

    failure_reason  TEXT,

    -- Reserve→settle/refund economy stamps (spec §3): displayed price + the
    -- deterministic reservation handle (proofing_test_gen:tenant:gcid:id).
    mana_reserved   BIGINT       NOT NULL DEFAULT 0 CHECK (mana_reserved >= 0),
    reservation_id  TEXT,

    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,                        -- soft-delete (#5)

    -- Shape guards (fail-loud at the DB): a ready row carries its composed
    -- payload; a failed row carries its reason.
    CONSTRAINT proofing_tests_ready_shape CHECK (
        status <> 'ready' OR testset_payload IS NOT NULL
    ),
    CONSTRAINT proofing_tests_failed_shape CHECK (
        status <> 'failed' OR failure_reason IS NOT NULL
    )
);

-- The qgen terminal events (completed/refused) key back on the batch ref —
-- one live proofing test per assist_id per tenant.
CREATE UNIQUE INDEX IF NOT EXISTS idx_proofing_tests_assist
    ON proofing_tests (tenant_id, assist_id)
    WHERE deleted_at IS NULL;

-- Learner list read (GET /v1/me/proofing-tests?goal_id=), newest first.
CREATE INDEX IF NOT EXISTS idx_proofing_tests_learner
    ON proofing_tests (tenant_id, learner_gcid, created_at DESC)
    WHERE deleted_at IS NULL;

ALTER TABLE proofing_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE proofing_tests FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON proofing_tests;
CREATE POLICY tenant_isolation ON proofing_tests
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

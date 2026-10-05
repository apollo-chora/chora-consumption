-- =============================================================================
-- chora-consumption : 0060_concept_suggestions.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-212 WS-4 (D4/D9) — the Familiar SUGGESTION seam. The demoted "fog"
-- (ADR-143) now proposes candidate CONCEPTS + EDGES over the learner-sovereign
-- map; the learner curates (Accept → mints a concept_nodes / concept_edges row
-- with provenance='familiar_suggested_accepted'; Dismiss → status flip). See
-- internal/domain/concept_graph/suggestion.go.
--
-- Conventions mirror 0056_discovery_concept_graph.up.sql: soft-delete only
-- (deleted_at) #5; UUID default gen_random_uuid() but the authoritative id is a
-- UUIDv7 minted in the domain (#7); atom + concept references are opaque UUIDs
-- with NO FK (#3, cross-aggregate); RLS ENABLE + FORCE. Grants via
-- 9999_grant_app_roles.sql. NB: there is NO exactly-6 cap — ADR-212 D2/D6
-- replaces ADR-143's hard cap with a presentation-layer soft cap.
-- =============================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- concept_suggestions — a Familiar-proposed concept or edge awaiting learner
-- curation. Exactly one payload shape is populated per `kind` (enforced by the
-- two shape CHECKs). model_id/run_id are ADR-197 decision-stamps; rationale is
-- the learner-facing "why" (ADR-215); source_event_id is the originating
-- emitted-event id for idempotent ingestion.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS concept_suggestions (
    id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID         NOT NULL,
    learner_gcid       UUID         NOT NULL,
    kind               VARCHAR(16)  NOT NULL CHECK (kind IN ('concept', 'edge')),
    status             VARCHAR(16)  NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending', 'accepted', 'dismissed')),

    -- concept payload (kind = 'concept')
    title              TEXT,
    atom_refs          UUID[]       NOT NULL DEFAULT '{}',   -- 0..N LearningAtom refs; cross-DB, no FK (#3)

    -- edge payload (kind = 'edge'); opaque concept refs, app-validated (no FK, #3)
    source_concept_id  UUID,
    target_concept_id  UUID,
    edge_class         VARCHAR(16)  CHECK (edge_class IS NULL OR edge_class IN ('hierarchy', 'lateral')),

    -- explainability + provenance stamps
    rationale          TEXT,                                -- learner-facing "why" (ADR-215)
    model_id           TEXT,                                -- ADR-197 decision-stamp
    run_id             TEXT,                                -- ADR-197 decision-stamp
    source_familiar_id UUID,                                -- which Familiar proposed it
    source_event_id    UUID,                                -- originating emitted-event id (idempotent ingest)

    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    decided_at         TIMESTAMPTZ,                         -- set when accepted/dismissed
    deleted_at         TIMESTAMPTZ,                         -- soft-delete (#5)

    -- Kind-shape guards (fail-loud at the DB): a concept suggestion carries a
    -- title and no edge endpoints; an edge suggestion carries endpoints + class
    -- and no self-loop.
    CONSTRAINT concept_suggestions_concept_shape CHECK (
        kind <> 'concept' OR (
            title IS NOT NULL
            AND source_concept_id IS NULL
            AND target_concept_id IS NULL
            AND edge_class IS NULL)
    ),
    CONSTRAINT concept_suggestions_edge_shape CHECK (
        kind <> 'edge' OR (
            source_concept_id IS NOT NULL
            AND target_concept_id IS NOT NULL
            AND edge_class IS NOT NULL
            AND source_concept_id <> target_concept_id)
    )
);

-- Fast path for the pending-suggestions list (the learner's inbox).
CREATE INDEX IF NOT EXISTS idx_concept_suggestions_pending
    ON concept_suggestions (tenant_id, learner_gcid)
    WHERE status = 'pending' AND deleted_at IS NULL;

-- Idempotent ingest: a single emitted event carries N suggestions (all sharing
-- its event id), so this is a PLAIN index — the subscriber enforces idempotency
-- by probing GetBySourceEvent (if any live row for the event exists, the batch
-- was already ingested → ack + skip) rather than a DB unique constraint.
CREATE INDEX IF NOT EXISTS idx_concept_suggestions_source_event
    ON concept_suggestions (tenant_id, learner_gcid, source_event_id)
    WHERE source_event_id IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE concept_suggestions ENABLE ROW LEVEL SECURITY;
ALTER TABLE concept_suggestions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON concept_suggestions;
CREATE POLICY tenant_isolation ON concept_suggestions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

-- =============================================================================
-- chora-consumption : 0056_discovery_concept_graph.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- ADR-212 WS-1 (Learner-Sovereign Discovery Knowledge Graph) — the learner-owned
-- ConceptNode + Edge aggregates. Supersedes ADR-143's machine-authored fog
-- MODEL (projection-over-atoms / machine-only-provenance / exactly-6); the KG
-- relocation to chora_consumption remains in force. See
-- internal/domain/concept_graph/{concept_node,edge}.go.
--
-- Conventions mirror 0051_goals.up.sql: soft-delete only (deleted_at) #5;
-- UUID default gen_random_uuid() but the authoritative id is a UUIDv7 minted in
-- the domain (#7); atom + cross-concept references are opaque UUIDs with NO FK
-- (#3, cross-aggregate); RLS-enabled (ENABLE + FORCE). Grants handled by
-- 9999_grant_app_roles.sql (no explicit GRANT here).
-- =============================================================================
BEGIN;

-- ---------------------------------------------------------------------------
-- concept_nodes — a learner-named idea that ORGANISES 0..N LearningAtoms.
-- atom_refs may be empty (ADR-212 D1 — a concept can be temporarily empty).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS concept_nodes (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL,
    learner_gcid  UUID         NOT NULL,
    title         TEXT         NOT NULL,
    atom_refs     UUID[]       NOT NULL DEFAULT '{}',   -- 0..N LearningAtom refs; cross-DB, no FK (#3); may be empty (D1)
    provenance    VARCHAR(32)  NOT NULL DEFAULT 'learner_authored'
                               CHECK (provenance IN (
                                   'learner_authored',
                                   'familiar_suggested_accepted',
                                   'system_derived')),
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ                           -- soft-delete (#5)
);

CREATE INDEX IF NOT EXISTS idx_concept_nodes_learner
    ON concept_nodes (tenant_id, learner_gcid) WHERE deleted_at IS NULL;

ALTER TABLE concept_nodes ENABLE ROW LEVEL SECURITY;
ALTER TABLE concept_nodes FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON concept_nodes;
CREATE POLICY tenant_isolation ON concept_nodes
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- ---------------------------------------------------------------------------
-- concept_edges — a learner-authored typed relation between two ConceptNodes.
-- Endpoints are opaque concept UUIDs with NO FK (separate aggregates; re-root +
-- soft-delete must not be blocked by referential integrity, #3).
-- NB: there is deliberately NO hard cap on relation count — ADR-212 D2/D6
-- replaces ADR-143's exactly-6 CHECK with a SOFT cap enforced in the
-- presentation layer (hex face + overflow drawer), NOT the database.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS concept_edges (
    id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID         NOT NULL,
    learner_gcid       UUID         NOT NULL,
    source_concept_id  UUID         NOT NULL,   -- intra-domain concept ref, app-validated (no FK, #3)
    target_concept_id  UUID         NOT NULL,
    edge_class         VARCHAR(16)  NOT NULL
                                    CHECK (edge_class IN ('hierarchy', 'lateral')),
    provenance         VARCHAR(32)  NOT NULL DEFAULT 'learner_authored'
                                    CHECK (provenance IN (
                                        'learner_authored',
                                        'familiar_suggested_accepted',
                                        'system_derived')),
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,                       -- soft-delete (#5)
    CONSTRAINT concept_edges_no_self_loop CHECK (source_concept_id <> target_concept_id)
);

CREATE INDEX IF NOT EXISTS idx_concept_edges_learner
    ON concept_edges (tenant_id, learner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_edges_source
    ON concept_edges (tenant_id, learner_gcid, source_concept_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_edges_target
    ON concept_edges (tenant_id, learner_gcid, target_concept_id) WHERE deleted_at IS NULL;

-- One live relation per (source, target, class) for a learner (re-authoring the
-- same edge is idempotent). Partial so a soft-deleted edge doesn't block a redo.
CREATE UNIQUE INDEX IF NOT EXISTS uq_concept_edges_live_relation
    ON concept_edges (tenant_id, learner_gcid, source_concept_id, target_concept_id, edge_class)
    WHERE deleted_at IS NULL;

ALTER TABLE concept_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE concept_edges FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON concept_edges;
CREATE POLICY tenant_isolation ON concept_edges
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

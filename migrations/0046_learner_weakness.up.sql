-- =============================================================================
-- chora-consumption : 0046_learner_weakness.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : Epic-1 1b — Growth-Edge (LearnerWeakness) layer, 2026-06-09
-- Architecture  : ~/.claude/plans/warm-drifting-crab.md (W1)
--                 chora-contracts/proto/events/consumption/weakness.proto (FROZEN)
--                 chora-contracts/openapi/consumption-growth-edges.yaml (FROZEN)
--                 internal/domain/learner_weakness/* (aggregate + ports)
--
-- Purpose:
--   Per-GCID "Growth Edge" store — the learner's persistent map of where they
--   are shaky. Fed by explicit uploads (analysed by the ai-kernel weakness-
--   analyser) + derived performance signals (topic_accuracy / Ebbinghaus
--   retention / classroom live-quiz), source-tagged + folded together.
--
--   FLAT model (no parent/child hierarchy): concepts are de-duplicated by
--   concept_embedding cosine similarity (>= 0.9 ⟺ distance <= 0.1 → merge) and
--   rolled up by category / tags / topic_id at read time.
--
-- Soft-delete only (`deleted_at`) per .claude/rules/ddd-enforcement.md §5.
-- RLS-enabled — composes with the multi-tenant-rls skill (every read/write runs
-- rls.ApplySession first). UUID default + UUIDv7 minted in the domain layer.
-- =============================================================================

BEGIN;

-- Defensive: pgvector is already enabled by chora-creation 0001 + reused by the
-- F4 familiar_memory_recall table (0045); a fresh-DB bootstrap must not fail.
CREATE EXTENSION IF NOT EXISTS "vector";

-- -----------------------------------------------------------------------------
-- learner_weakness — one row per (learner, distinct weak concept).
--
-- concept_embedding is the 768-d text-embedding-004 vector of concept_label,
-- used for dedup (cosine), nearest-TopicNode resolution, and semantic drill-atom
-- retrieval. descriptor is distilled metadata (summary / misconceptions /
-- sample_wrong / suggested_angles / per_item_correctness) — NEVER the raw upload.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS learner_weakness (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID         NOT NULL,
    learner_gcid          UUID         NOT NULL,
    concept_key           TEXT         NOT NULL,                  -- normalised slug
    concept_label         TEXT         NOT NULL,                  -- Familiar-facing phrase
    concept_embedding     vector(768)  NOT NULL,
    topic_id              UUID,                                   -- nearest TopicNode (nullable)
    category              TEXT,                                   -- coarse bucket (nullable)
    tags                  TEXT[]       NOT NULL DEFAULT '{}',
    strength              REAL         NOT NULL CHECK (strength >= 0 AND strength <= 1),
    sources               TEXT[]       NOT NULL DEFAULT '{}',     -- explicit|derived|classroom
    descriptor            JSONB        NOT NULL DEFAULT '{}'::jsonb,
    cached_drill_atom_ids UUID[]       NOT NULL DEFAULT '{}',     -- W4 semantic cache
    status                VARCHAR(16)  NOT NULL DEFAULT 'active'
                                       CHECK (status IN ('active', 'grown')),
    first_seen_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_evidenced_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ                             -- soft-delete per ddd-enforcement §5
);

-- IVFFLAT cosine index — mirrors familiar_memory_recall + chora-creation
-- atom_embeddings (lists = 100; revisit at M14 once row counts grow).
CREATE INDEX IF NOT EXISTS idx_learner_weakness_cosine
    ON learner_weakness USING ivfflat (concept_embedding vector_cosine_ops) WITH (lists = 100);

-- Hot path: dedup nearest-scan + the Growth-Edges list are both per-learner over
-- live rows.
CREATE INDEX IF NOT EXISTS idx_learner_weakness_learner
    ON learner_weakness (tenant_id, learner_gcid) WHERE deleted_at IS NULL;

-- Exact concept-key lookup (secondary dedup fast-path + admin/debug).
CREATE INDEX IF NOT EXISTS idx_learner_weakness_concept_key
    ON learner_weakness (learner_gcid, concept_key) WHERE deleted_at IS NULL;

-- Category/topic rollup filters on the list endpoint.
CREATE INDEX IF NOT EXISTS idx_learner_weakness_category
    ON learner_weakness (learner_gcid, category) WHERE deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- multi-tenant-rls skill: every read/write runs SET LOCAL chora.tenant_id
-- (rls.ApplySession) BEFORE the user query. Per-learner scoping is an explicit
-- learner_gcid predicate in every query (RLS isolates the tenant; the learner is
-- the validated session subject).
-- -----------------------------------------------------------------------------
ALTER TABLE learner_weakness ENABLE ROW LEVEL SECURITY;
ALTER TABLE learner_weakness FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON learner_weakness;
CREATE POLICY tenant_isolation ON learner_weakness
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- (re-runs lex-last on every migration job; ALTER DEFAULT PRIVILEGES grants
-- app_rw / app_ro on new tables automatically — no explicit GRANT here).

COMMIT;

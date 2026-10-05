-- =============================================================================
-- chora-consumption : 0045_familiar_memory_recall.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-173 — Familiar memory pgvector RAG (F4, 2026-06-02)
-- Architecture  : docs/architecture/adrs/adr-173-familiar-memory-pgvector-rag.md
--                 chora-contracts/openapi/consumption-familiar-chat.yaml (F4 §RAG)
--                 internal/domain/familiar/memory.go (FamiliarMemory port)
--                 internal/domain/familiar/embedder.go (Embedder port — peer)
--
-- Purpose:
--   Per-Familiar RAG memory store (F4). Each conversational turn is embedded
--   (text-embedding-004, 768-d) and persisted here; the next turn embeds the
--   incoming message and recalls the nearest prior memories by cosine distance.
--   Scope is per-Familiar (`scope_key = 'familiar:{familiar_id}'`) mirroring the
--   agent's app_name prefix — NOT per-user-shared (multi-Familiar 1:N per ADR-147).
--
-- Soft-delete only (`deleted_at`) per .claude/rules/ddd-enforcement.md §5.
-- TTL via `expires_at` (NULL = no expiry). RLS-enabled — composes with the
-- multi-tenant-rls skill (every read/write runs rls.ApplySession first).
-- =============================================================================

BEGIN;

-- Defensive: pgvector is already enabled by chora-creation 0001; chora_consumption
-- shares the same Cloud SQL instance + extension set, but a fresh-DB bootstrap of
-- chora_consumption alone (CI / local) must not fail on a missing extension.
CREATE EXTENSION IF NOT EXISTS "vector";   -- defensive; already enabled in 0001

-- -----------------------------------------------------------------------------
-- familiar_memory_recall — one row per recorded per-Familiar memory.
--
-- embedding is the 768-d Vertex AI text-embedding-004 vector for content_text.
-- scope_key = 'familiar:{familiar_id}' (canonical via familiar.MemoryScopeKey).
-- expires_at carries an optional TTL; NULL = retained indefinitely (until soft
-- delete or crypto-shred on closure).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_memory_recall (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL,
    owner_gcid        UUID         NOT NULL,
    familiar_id       UUID         NOT NULL,
    scope_key         TEXT         NOT NULL,            -- 'familiar:{familiar_id}'
    memory_type       VARCHAR(32)  NOT NULL DEFAULT 'chat_turn',
    content_text      TEXT         NOT NULL CHECK (length(content_text) > 0),
    embedding         vector(768)  NOT NULL,
    model_id          VARCHAR(64)  NOT NULL,
    source_turn_id    UUID,
    source_session_id UUID,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ,                      -- TTL; NULL = no expiry
    deleted_at        TIMESTAMPTZ                       -- soft-delete per ddd-enforcement §5
);

-- IVFFLAT cosine index — mirrors chora-creation atom_embeddings (lists = 100,
-- tuned for ~10k rows; revisit at M14).
CREATE INDEX IF NOT EXISTS idx_familiar_memory_recall_cosine
    ON familiar_memory_recall USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- Hot-path recall filter: Recall scans by (tenant, familiar) live rows.
CREATE INDEX IF NOT EXISTS idx_familiar_memory_recall_familiar
    ON familiar_memory_recall (tenant_id, familiar_id) WHERE deleted_at IS NULL;

-- TTL sweep path (M17 maintenance job expires aged memories).
CREATE INDEX IF NOT EXISTS idx_familiar_memory_recall_expiry
    ON familiar_memory_recall (expires_at) WHERE deleted_at IS NULL AND expires_at IS NOT NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- multi-tenant-rls skill: every read/write runs SET LOCAL chora.tenant_id
-- BEFORE the user query (rls.ApplySession on the open transaction).
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_memory_recall ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_memory_recall FORCE ROW LEVEL SECURITY;
-- DROP+CREATE for re-apply idempotency (CREATE POLICY has no IF NOT EXISTS).
DROP POLICY IF EXISTS tenant_isolation ON familiar_memory_recall;
CREATE POLICY tenant_isolation ON familiar_memory_recall
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- which re-runs lex-last on every migration job + sets ALTER DEFAULT
-- PRIVILEGES for the migrate role. New tables created by this migration
-- automatically inherit the app_rw / app_ro grants via that default-privileges
-- mechanism — no explicit GRANT here is needed.

COMMIT;

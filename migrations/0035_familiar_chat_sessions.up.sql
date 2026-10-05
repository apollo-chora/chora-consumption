-- =============================================================================
-- chora-consumption : 0035_familiar_chat_sessions.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-154 — Familiar conversational chat surface (2026-05-15)
-- Architecture  : docs/architecture/adrs/adr-154-familiar-chat-surface.md
--                 chora-contracts/openapi/consumption-familiar-chat.yaml
--                 chora-contracts/proto/events/consumption/familiar.proto §ADR-154
--
-- Purpose:
--   Long-lived ADK session table for the Familiar chat surface (per
--   ADR-154 D2). One open session per (owner_gcid, familiar_id); turns
--   reuse engine_session_id across the conversation history.
--
-- Soft-delete only (`closed_at`) per .claude/rules/ddd-enforcement.md.
-- RLS-enabled — composes with multi-tenant-rls skill.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- familiar_chat_sessions — long-lived ADK session per (gcid, familiar_id)
--
-- engine_session_id is the opaque Vertex AI Agent Engine session id returned
-- by `CreateSession` on the deployed familiar_companion engine. Reused across
-- turns so the engine accumulates conversation history (ADK session.State
-- + Events facility).
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_chat_sessions (
    -- UUIDv7 — sortable + tenant-opaque per CLAUDE.md domain vocabulary.
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),

    tenant_id           UUID         NOT NULL,
    owner_gcid          UUID         NOT NULL,
    familiar_id         UUID         NOT NULL,

    -- Vertex AI Agent Engine session_id (opaque string, supplied by Vertex).
    -- May exceed 64 bytes; TEXT is appropriate.
    engine_session_id   TEXT         NOT NULL CHECK (length(engine_session_id) > 0),

    -- Per-session running totals — denormalized cache for the C+ chat thread
    -- header UI; source-of-truth is the per-turn ledger in BigQuery via the
    -- FamiliarChatTurnCompleted event stream.
    mana_charged_total  INTEGER      NOT NULL DEFAULT 0 CHECK (mana_charged_total >= 0),
    turn_count          INTEGER      NOT NULL DEFAULT 0 CHECK (turn_count >= 0),

    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Soft-delete per .claude/rules/ddd-enforcement.md §5. NULL = open.
    -- A maintenance sweeper (M17) closes sessions with last_used_at < now() - 30d.
    closed_at           TIMESTAMPTZ
);

-- -----------------------------------------------------------------------------
-- Indexes
-- -----------------------------------------------------------------------------

-- Hot-path lookup: chora-consumption Server.handleFamiliarChat resolves the
-- open session for (owner_gcid, familiar_id) on every turn. UNIQUE WHERE
-- closed_at IS NULL enforces ADR-154 D2: at most ONE open session per pair.
CREATE UNIQUE INDEX idx_familiar_chat_sessions_open_per_user
    ON familiar_chat_sessions (owner_gcid, familiar_id)
    WHERE closed_at IS NULL;

-- Tenant-scoped sweep + audit query path (M17 maintenance jobs).
CREATE INDEX idx_familiar_chat_sessions_tenant_last_used
    ON familiar_chat_sessions (tenant_id, last_used_at)
    WHERE closed_at IS NULL;

-- Backwards correlation: when the engine returns `session_not_found` we
-- need to surface "which engine session was lost" for ops triage.
CREATE INDEX idx_familiar_chat_sessions_engine_session
    ON familiar_chat_sessions (engine_session_id)
    WHERE closed_at IS NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- multi-tenant-rls skill: every read/write runs SET LOCAL chora.tenant_id
-- BEFORE the user query (rls.ApplySession on the open transaction).
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_chat_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_chat_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_chat_sessions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- which re-runs lex-last on every migration job + sets ALTER DEFAULT
-- PRIVILEGES for the migrate role. New tables created by this migration
-- automatically inherit the app_rw / app_ro grants via that default-privileges
-- mechanism — no explicit GRANT here is needed.

COMMIT;

-- =============================================================================
-- chora-consumption : 0111_companion_turns.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Story         : ADR-254 D4 (caller-facing workflow lanes) + D8 (chat on the bus)
-- Date          : 2026-08-23
--
-- Purpose:
--   The per-request STATE of every agent turn consumption dispatches over the
--   bus (typed chat, skill invoke, ceremony edge scout, ritual step, daily-dose
--   greeting on chora.consumption.companion_turn.requested.v1; the daily-dose
--   recommendation on chora.consumption.dose_recommendation.requested.v1).
--
--   A turn row is written in the SAME transaction as its outbox request row
--   (accepted = durably requested); the pull consumers on
--   chora-consumption.companion-turn-completed / .dose-recommendation-completed
--   mark it completed | failed | rejected with the result body; the HTTP waiter
--   polls the row (any pod may receive the completion) and the SSE chat replays
--   a retried turn_id from the stored result instead of re-publishing (the
--   kennel dedupes the request key for seven days and never re-dispatches).
--   A turn still accepted past deadline_at is marked timeout by the waiter.
--
-- Shape follows the 0056 idiom: RLS ENABLE+FORCE + tenant_isolation on
-- chora.tenant_id, UUIDv7 ids minted in Go, live-row partial indexes, grants via
-- 9999. Request/result bodies are JSONB (the lane is JSON-wire, ADR-254 D4).
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS companion_turns (
    turn_id                UUID        PRIMARY KEY,
    tenant_id              UUID        NOT NULL,
    owner_gcid             UUID        NOT NULL,
    -- NULL for a dose recommendation (per learner, not per Companion).
    companion_id           UUID,
    -- The consumption chat-session row id for typed chat (the kennel/agent
    -- persists the conversation under it); NULL for the other kinds.
    conversation_id        UUID,
    kind                   TEXT        NOT NULL
        CHECK (kind IN ('typed', 'skill', 'ceremony', 'ritual', 'greeting', 'dose')),
    lane                   TEXT        NOT NULL
        CHECK (lane IN ('companion_turn', 'dose_recommendation')),
    status                 TEXT        NOT NULL DEFAULT 'accepted'
        CHECK (status IN ('accepted', 'completed', 'failed', 'rejected', 'timeout')),
    request                JSONB       NOT NULL,
    result                 JSONB,
    error_code             TEXT,
    workflow_id            TEXT,
    generated_by_model_id  TEXT,
    requested_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at            TIMESTAMPTZ NOT NULL,
    completed_at           TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT companion_turns_terminal_has_completed_at
        CHECK (status = 'accepted' OR completed_at IS NOT NULL),
    CONSTRAINT companion_turns_deadline_after_request
        CHECK (deadline_at > requested_at)
);

COMMENT ON TABLE companion_turns IS
  'ADR-254 D4/D8: per-request state of every agent turn consumption dispatches over the bus (companion_turn + dose_recommendation lanes); accepted = durably requested (same tx as the outbox row); replay by turn_id.';

-- Open turns per tenant (the waiter reaps by deadline; an ops view of backlog).
CREATE INDEX IF NOT EXISTS idx_companion_turns_open
    ON companion_turns (tenant_id, deadline_at)
    WHERE status = 'accepted';

-- A learner's turns newest-first (support/audit reads).
CREATE INDEX IF NOT EXISTS idx_companion_turns_owner
    ON companion_turns (owner_gcid, requested_at DESC);

-- Conversation history for typed chat.
CREATE INDEX IF NOT EXISTS idx_companion_turns_conversation
    ON companion_turns (conversation_id, requested_at)
    WHERE conversation_id IS NOT NULL;

ALTER TABLE companion_turns ENABLE ROW LEVEL SECURITY;
ALTER TABLE companion_turns FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON companion_turns
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

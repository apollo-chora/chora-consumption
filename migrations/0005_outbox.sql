-- =============================================================================
-- chora-consumption : 0005_outbox.sql
--
-- Adds the transactional outbox primitive to chora_consumption per the
-- Phyllis Wave-B service-wiring directive (`feedback_resilience_priority`).
--
-- Domain  : Content Consumption (5 core)
-- Database: chora_consumption
-- Author  : A-Service-Wiring-Content (Phyllis Wave-B)
-- Date    : 2026-05-11
--
-- HISTORY
--   2026-05-11 — Initial schema (chora-go-common Recorder/Relay compatible).
--   2026-05-12 — M12.3 W1b D6.2 canonical fields added:
--                  - tenant_id UUID NULL (D6.3 multi-tenant isolation indexing)
--                  - gcid UUID NULL (subject)
--                  - idempotency_key TEXT NULL (dedupe key per CLAUDE.md §6)
--                  - tenant_idx (tenant_id, status, occurred_at)
--                  - idempotency_idx UNIQUE WHERE idempotency_key IS NOT NULL
--                  - RLS policy on outbox_events (permissive when GUC unset)
--
--                These ADD COLUMNs are additive + IF NOT EXISTS so existing
--                rows written by the chora-go-common/outbox CloudPublisher
--                path remain valid (NULL tenant/gcid/idempotency_key are
--                interpreted as legacy rows by the canonical D6.2 dispatcher
--                in services/chora-consumption/internal/adapter/outbox).
--
-- Schema follows the canonical D6.2 template mirrored from
-- services/chora-guardrail/migrations/0004_outbox.sql (M12.1 close).
--
-- HARD INVARIANT: outbox rows live in the SAME database as the domain
-- they serve (chora_consumption). Cross-DB queries remain forbidden.
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS outbox_events (
    id              TEXT        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    topic           TEXT        NOT NULL,
    payload         BYTEA       NOT NULL,
    envelope        JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','published','failed','deadlettered')),
    retry_count     INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    last_attempt_at TIMESTAMPTZ,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- M12.3 W1b — Canonical D6.2 fields (additive ALTER for backward compat).
-- tenant_id is the load-bearing multi-tenant isolation column (D6.3 indexed).
-- gcid is the subject GCID; nullable for system events.
-- idempotency_key dedupes re-emissions of the same logical event.
ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS tenant_id        UUID,
    ADD COLUMN IF NOT EXISTS gcid             UUID,
    ADD COLUMN IF NOT EXISTS idempotency_key  TEXT;

CREATE INDEX IF NOT EXISTS outbox_events_pending_idx
    ON outbox_events (occurred_at ASC)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS outbox_events_aggregate_idx
    ON outbox_events (aggregate_type, aggregate_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS outbox_events_topic_idx
    ON outbox_events (topic, status);

-- D6.3 multi-tenant isolation lookup: dispatcher MAY filter per-tenant.
CREATE INDEX IF NOT EXISTS outbox_events_tenant_idx
    ON outbox_events (tenant_id, status, occurred_at);

-- Idempotency dedupe — partial unique to allow legacy rows with NULL key.
CREATE UNIQUE INDEX IF NOT EXISTS outbox_events_idempotency_idx
    ON outbox_events (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS outbox_poll_checkpoints (
    worker_id                  TEXT        NOT NULL,
    topic                      TEXT        NOT NULL,
    last_processed_outbox_id   TEXT        NOT NULL,
    last_processed_occurred_at TIMESTAMPTZ NOT NULL,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (worker_id, topic)
);

CREATE INDEX IF NOT EXISTS outbox_poll_checkpoints_topic_idx
    ON outbox_poll_checkpoints (topic, updated_at DESC);

CREATE TABLE IF NOT EXISTS outbox_dead_letters (
    outbox_event_id   TEXT        PRIMARY KEY REFERENCES outbox_events(id),
    failure_reason    TEXT        NOT NULL,
    attempt_count     INT         NOT NULL,
    worker_id         TEXT        NOT NULL,
    deadlettered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at       TIMESTAMPTZ,
    resolution_note   TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS outbox_dead_letters_unresolved_idx
    ON outbox_dead_letters (deadlettered_at DESC)
    WHERE resolved_at IS NULL;

-- Multi-tenant RLS — same pattern as chora-guardrail / chora-closure-orchestrator
-- / chora-ai-kernel-orchestrator outbox tables. chora-consumption serves many
-- tenants per pod; production wires the GUC via per-connection
-- SET LOCAL app.current_tenant. Policy is permissive when the GUC is unset
-- to keep the dispatcher (a non-tenant-scoped background worker) functional;
-- the application + envelope + protobuf layers enforce isolation defence-in-depth.
ALTER TABLE outbox_events ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS outbox_events_tenant_isolation ON outbox_events;
CREATE POLICY outbox_events_tenant_isolation ON outbox_events
    USING (
        current_setting('app.current_tenant', TRUE) IS NULL
        OR current_setting('app.current_tenant', TRUE) = ''
        OR tenant_id IS NULL
        OR tenant_id::TEXT = current_setting('app.current_tenant', TRUE)
    );

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           TEXT        PRIMARY KEY,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ttl_at        TIMESTAMPTZ NOT NULL,
    result_hash   TEXT        NULL
);

CREATE INDEX IF NOT EXISTS idempotency_keys_ttl_idx
    ON idempotency_keys (ttl_at);

COMMIT;

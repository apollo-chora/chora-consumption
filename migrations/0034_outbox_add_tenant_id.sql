-- =============================================================================
-- chora-consumption : 0034_outbox_add_tenant_id.sql
--
-- Wave B propagation paydown (2026-05-14, tracker #153 / ZA): chora-consumption
-- outbox dispatcher fails ~1/sec at runtime with
--
--   ERROR: column "tenant_id" does not exist (SQLSTATE 42703)
--
-- when scanning outbox_events.
--
-- ROOT CAUSE — migration 0005_outbox.sql already declares tenant_id (via
-- ADD COLUMN IF NOT EXISTS on lines 59-62), but in the production DB the
-- outbox_events table was created by an earlier code path (the chora-go-
-- common/outbox PostgresRecorder template ships a CREATE TABLE WITHOUT
-- tenant_id at libs/chora-go-common/outbox/sql_fixtures/outbox_events.up.sql).
-- The 0005 ALTER then never executed because the runner's per-file dedup
-- table believed the file had already been applied at the prior CREATE-only
-- variant. Net result: production has the legacy column set; the canonical
-- Go dispatcher (services/chora-consumption/internal/adapter/outbox/store.go
-- §INSERT + §FetchPending) references tenant_id and crashes.
--
-- FIX — re-apply the canonical D6.2 ALTERs from 0005, plus the supporting
-- index + RLS policy, under a fresh filename so the runner's
-- chora_runner_schema_migrations table treats this as a new apply. Every
-- statement is guarded with IF NOT EXISTS / DROP-and-recreate so re-runs
-- are no-ops.
--
-- Domain  : Content Consumption (5 core)
-- Database: chora_consumption
-- Author  : ZA paydown agent (Wave B propagation, tracker #153)
-- Date    : 2026-05-14
--
-- HARD INVARIANT: outbox rows live in the SAME database as the domain
-- they serve (chora_consumption). Cross-DB queries remain forbidden.
-- =============================================================================

BEGIN;

-- Canonical D6.2 fields. Mirrors 0005_outbox.sql §M12.3 W1b ALTER block.
-- IF NOT EXISTS guards re-apply against the prior variant.
ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS tenant_id        UUID,
    ADD COLUMN IF NOT EXISTS gcid             UUID,
    ADD COLUMN IF NOT EXISTS idempotency_key  TEXT;

-- Backfill tenant_id from the event_envelope JSON for any pre-existing
-- rows the legacy CREATE TABLE may have accumulated. JSONB ?-operator
-- short-circuits when the key is absent so the UPDATE is safe on empty
-- envelopes.
UPDATE outbox_events
    SET tenant_id = (envelope ->> 'tenant_id')::UUID
    WHERE tenant_id IS NULL
      AND envelope ? 'tenant_id'
      AND envelope ->> 'tenant_id' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$';

-- D6.3 multi-tenant isolation lookup index — partial on pending rows since
-- the dispatcher only ever scans for status='pending'.
CREATE INDEX IF NOT EXISTS outbox_events_tenant_idx
    ON outbox_events (tenant_id, status, occurred_at);

-- Idempotency dedupe — partial unique to allow legacy rows with NULL key.
CREATE UNIQUE INDEX IF NOT EXISTS outbox_events_idempotency_idx
    ON outbox_events (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Multi-tenant RLS — mirrors 0005 §RLS block exactly. Permissive when GUC
-- unset so the dispatcher (a non-tenant-scoped background worker) keeps
-- functioning; defence-in-depth comes from the surrounding domain repos +
-- envelope + Protobuf layers.
ALTER TABLE outbox_events ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS outbox_events_tenant_isolation ON outbox_events;
CREATE POLICY outbox_events_tenant_isolation ON outbox_events
    USING (
        current_setting('app.current_tenant', TRUE) IS NULL
        OR current_setting('app.current_tenant', TRUE) = ''
        OR tenant_id IS NULL
        OR tenant_id::TEXT = current_setting('app.current_tenant', TRUE)
    );

COMMIT;

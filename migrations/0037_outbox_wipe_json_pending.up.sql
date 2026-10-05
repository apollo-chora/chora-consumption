-- 0037_outbox_wipe_json_pending.up.sql
--
-- Outbox payload encoding migration (codebase-wide JSON→binary-protobuf fix).
--
-- Background
-- ----------
-- Pre-fix outbox rows on chora_consumption.outbox_events held JSON-marshalled
-- payload bytes that the BINARY schema encoding rejects at publish time with
-- "Invalid binary proto message". The dispatcher retries
-- forever (MaxAttempts=5 then deadletter) and the row never publishes.
--
-- Fix
-- ---
-- internal/adapter/events/protomarshal now emits canonical binary protobuf
-- bytes for the two BINARY-encoded topics surfaced in the 2026-05-15 final
-- report:
--
--   * chora.consumption.daily_dose.served.v1
--   * chora.consumption.familiar.chat_turn_completed.v1
--
-- New rows written after the fix carry binary bytes and publish cleanly.
-- This migration drains pre-fix JSON-payload rows out of the pending queue
-- so the dispatcher stops retrying them; rows that NEVER successfully
-- published (still 'pending') are safe to mark 'failed' — no downstream
-- subscriber ever saw them.
--
-- Replay strategy
-- ---------------
-- We mark-failed rather than translate-and-retry: the producer-side handlers
-- (familiar_chat_handler, familiar_handlers.dailyDose) are idempotent on
-- envelope.idempotency_key — re-triggering the user flow will emit a fresh
-- correctly-encoded outbox row. Translating JSON-decoded fields back into
-- the typed proto would be more error-prone than re-emission, and these
-- topics carry zero state-change semantics beyond the engagement+cost
-- telemetry feed.
--
-- Idempotent: re-running is a no-op (the WHERE clause matches no rows after
-- the first pass).
UPDATE outbox_events
SET status          = 'failed',
    last_error      = 'codebase-wide outbox protobuf encoding fix #33 — pre-fix JSON-payload row drained',
    last_attempt_at = now()
WHERE status = 'pending'
  AND topic IN (
    'chora.consumption.daily_dose.served.v1',
    'chora.consumption.familiar.chat_turn_completed.v1'
  );

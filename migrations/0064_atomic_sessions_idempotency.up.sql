-- 0064 — CHO-2029: durable atom sessions. The atomic_sessions table has
-- existed since 0001 but the pg adapter was never wired (sessions were
-- in-memory per pod; 2 replicas => intermittent SESSION_NOT_FOUND). The
-- S4.2 idempotent-answer fields never made it to the table — add them so
-- a cross-pod replay of the same answer_id stays a duplicate no-op.
ALTER TABLE atomic_sessions
    ADD COLUMN IF NOT EXISTS last_answer_id     TEXT    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_correct       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS last_result_status TEXT    NOT NULL DEFAULT '';

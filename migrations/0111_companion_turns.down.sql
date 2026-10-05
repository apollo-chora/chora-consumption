-- 0111_companion_turns.down.sql: reverses 0111_companion_turns.up.sql.
-- The table holds per-request workflow state only (request + result bodies of
-- bus-dispatched turns); the outbox rows and the kennel's own ledger are the
-- durable record of what was dispatched, so dropping this table loses no source
-- of truth. Down migrations are never run by the runner lane (operator action).
BEGIN;
DROP POLICY IF EXISTS tenant_isolation ON companion_turns;
DROP INDEX IF EXISTS idx_companion_turns_conversation;
DROP INDEX IF EXISTS idx_companion_turns_owner;
DROP INDEX IF EXISTS idx_companion_turns_open;
DROP TABLE IF EXISTS companion_turns;
COMMIT;

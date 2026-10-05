-- =============================================================================
-- chora-consumption : 0035_familiar_chat_sessions.down.sql
--
-- Inverse of 0035_familiar_chat_sessions.up.sql — drops the table and all
-- its indexes. RLS policy auto-drops with the table.
-- =============================================================================

BEGIN;

DROP TABLE IF EXISTS familiar_chat_sessions;

COMMIT;

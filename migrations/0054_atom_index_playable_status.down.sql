-- =============================================================================
-- chora-consumption : 0054_atom_index_playable_status.down.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-1968, 2026-06-30
--
-- Purpose:
--   Reverse 0054 — drop the atom_index.status playability column.
-- =============================================================================

BEGIN;

ALTER TABLE atom_index
    DROP COLUMN IF EXISTS status;

COMMIT;

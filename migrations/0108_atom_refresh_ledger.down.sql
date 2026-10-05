-- chora-consumption : 0108_atom_refresh_ledger.down.sql
-- Reverses 0108: drops the ADR-244 D5 atom-refresh proposal ledger.

BEGIN;

DROP TABLE IF EXISTS atom_refresh_ledger;

COMMIT;

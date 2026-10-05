-- chora-consumption : 0080_campaign_reveal_ledger.down.sql  (reverse of 0080 up)
-- Drops the WS-C4 free-on-win fog-reveal ledger. The ledger is a dedupe record
-- only (no learner-durable state); a re-up rebuilds it empty and reveals
-- re-derive from campaign.node_won.v1 redelivery.
BEGIN;

DROP TABLE IF EXISTS campaign_reveal_ledger;

COMMIT;

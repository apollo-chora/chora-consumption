-- chora-consumption : 0078_campaign_counter_and_seal.down.sql  (reverse of 0078 up)
BEGIN;

ALTER TABLE goals DROP COLUMN IF EXISTS campaign_sealed_at;

ALTER TABLE campaign_node_progress DROP COLUMN IF EXISTS current_rung_correct;

COMMIT;

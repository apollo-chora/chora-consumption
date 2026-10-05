-- chora-consumption : 0077_familiar_campaign.down.sql  (reverse of 0077 up)
BEGIN;

ALTER TABLE goals DROP COLUMN IF EXISTS focus_concept_id;

DROP TABLE IF EXISTS concept_node_lineage;
DROP TABLE IF EXISTS campaign_node_progress;

COMMIT;

-- 0066_concept_node_intent.down.sql — reverse CHO-2038's additive intent column.
ALTER TABLE concept_nodes DROP COLUMN IF EXISTS intent;

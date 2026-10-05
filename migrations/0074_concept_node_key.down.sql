-- 0074_concept_node_key.down.sql — reverse of 0074. Drops the additive column;
-- ConceptNodes revert to un-keyed (the proofing R8-7 gate re-closes honestly).
ALTER TABLE concept_nodes DROP COLUMN IF EXISTS concept_key;

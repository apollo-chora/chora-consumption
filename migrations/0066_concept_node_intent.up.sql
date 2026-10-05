-- 0066_concept_node_intent.up.sql — CHO-2038: a ConceptNode minted from a
-- ceremony "learning-edge" selection carries a remediate|explore intent (ADR-212
-- Fork 2, "Both, labelled"). Additive + backfill-safe: existing rows default to
-- '' (unspecified — a plain, non-learning-edge concept). RLS is row-level and is
-- already enforced (FORCE) on concept_nodes, so a new column needs no policy
-- change. The CHECK keeps the column honest (only the three domain values).
ALTER TABLE concept_nodes
    ADD COLUMN IF NOT EXISTS intent VARCHAR(16) NOT NULL DEFAULT ''
        CHECK (intent IN ('', 'remediate', 'explore'));

COMMENT ON COLUMN concept_nodes.intent IS
    'ceremony learning-edge label (CHO-2038): remediate|explore; empty = plain concept';

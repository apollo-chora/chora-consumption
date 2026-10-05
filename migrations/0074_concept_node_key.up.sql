-- 0074_concept_node_key.up.sql — CHO-2038 key-at-mint: a ConceptNode carries a
-- STABLE normalised concept_key (the qgen `target_growth_edges` vocabulary that
-- the proofing R8-7 gate requires; shares the learner_weakness concept_key
-- vocabulary). Set once at mint from the title, stable across renames. This is
-- the "key-at-mint lands" the proofing_ticked_edges adapter was waiting for.
--
-- Additive + backfill-safe (mirrors 0066 intent + the 0046 backfill idiom). The
-- SQL normalisation is byte-identical to Go concept_graph.normalizeConceptKey /
-- learner_weakness.NormalizeConceptKey (lower-case → runs of non-alphanumerics
-- collapse to a single '-' → trim '-'). RLS row-level (FORCE) on concept_nodes;
-- a new column needs no policy change.
BEGIN;

ALTER TABLE concept_nodes
    ADD COLUMN IF NOT EXISTS concept_key TEXT NOT NULL DEFAULT '';

-- Backfill existing rows to the normalised slug of their title. concept_nodes is
-- FORCE ROW LEVEL SECURITY, so even the owner is row-filtered — toggle NO FORCE
-- for this owner-run one-time cross-tenant data fix, then restore immediately
-- (atomic within this transaction; mirrors 0046_familiar_species_hero backfill).
-- WITHOUT the toggle the UPDATE silently matches 0 rows. regexp_replace collapses
-- each run of non-[a-z0-9] to a single '-'; trim removes the leading/trailing
-- dashes Go never emits (it only emits '-' between kept runs).
ALTER TABLE concept_nodes NO FORCE ROW LEVEL SECURITY;
UPDATE concept_nodes
   SET concept_key = trim(both '-' from regexp_replace(lower(title), '[^a-z0-9]+', '-', 'g'))
 WHERE concept_key = '';
ALTER TABLE concept_nodes FORCE ROW LEVEL SECURITY;

COMMENT ON COLUMN concept_nodes.concept_key IS
    'CHO-2038 key-at-mint: stable normalised slug of title; qgen target_growth_edges key (proofing R8-7); shares learner_weakness concept_key vocabulary';

COMMIT;

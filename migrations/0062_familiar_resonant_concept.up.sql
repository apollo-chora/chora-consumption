-- =============================================================================
-- chora-consumption : 0062_familiar_resonant_concept.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Growth & Grimoire CR P1 "The Summoning & Awakening"
--            (CHO-2013, R3-1 ruling 2026-07-03; spec pack §5 P1-deltas block).
--
-- The awakening resonant-concept pick: the learner elects a ConceptNode
-- inside the attached Goal's subgraph as the Familiar's ring-radius centre.
-- UUID WITHOUT FK (cross-aggregate ref per ddd-enforcement #3 — concept_nodes
-- is the KG aggregate; validation happens in the domain service). NULL until
-- picked; the ring centre falls back to the Goal root while NULL; cleared on
-- rebind (the Summoner path). Legacy resonant_atom_id stays as hatch-flavour
-- provenance only (R3-1).
-- =============================================================================

ALTER TABLE familiar_instances
    ADD COLUMN IF NOT EXISTS resonant_concept_id UUID;

COMMENT ON COLUMN familiar_instances.resonant_concept_id IS
    'Awakening resonant-concept pick (R3-1, CHO-2013 P1): learner-elected ring-radius centre inside the bound Goal''s subgraph. ConceptNode ref, no FK. NULL = fall back to the Goal root. Cleared on rebind.';

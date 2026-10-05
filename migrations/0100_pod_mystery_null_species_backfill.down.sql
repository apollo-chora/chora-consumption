-- =============================================================================
-- chora-consumption : 0100_pod_mystery_null_species_backfill.down.sql
--
-- Story: CHO-2227. Deliberate NO-OP.
--
-- The up-migration NULLs the hidden species on unopened pods. There is nothing
-- to restore: the values it cleared were random rolls minted by the 0046 column
-- DEFAULT — rolled by nobody, shown to no one, and never the pod's real breed
-- (the real one is rolled at opening). Re-inventing them would fabricate data
-- that never had meaning, and would re-arm the bug this migration repairs.
--
-- To genuinely revert the BEHAVIOUR, revert the code change to
-- provisionEggInsertSQL (which names `species` NULL explicitly); the 0046
-- DEFAULT is untouched by this migration and would resume minting on omit.
-- =============================================================================

SELECT 1;

-- =============================================================================
-- chora-consumption : 0107_familiar_species_drop_random_default.down.sql
--
-- Restore the migration-0046 random-hero column DEFAULT on
-- familiar_instances.species.
--
-- WARNING: this re-arms the decoy. With the DEFAULT back, any INSERT that omits
-- `species` mints a breed nobody rolled, which the no-repeat ruling (ADR-248)
-- then reads as a species the learner OWNS: it is excluded from their next
-- ceremony's odds and an explicit pick of it is 409'd. Roll back only alongside
-- a rollback of the INSERT sites that name the column.
--
-- The array is reproduced verbatim from 0046 §2 so the rollback is exact.
-- =============================================================================

BEGIN;

ALTER TABLE familiar_instances
  ALTER COLUMN species SET DEFAULT
    (ARRAY['dragon','phoenix','owl','fox','penguin'])[1 + floor(random() * 5)::int];

COMMIT;

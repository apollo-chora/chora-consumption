-- =============================================================================
-- chora-consumption : 0046_familiar_species_hero_default_backfill.down.sql
-- Reverts 0046: drop the random-hero DEFAULT, remap penguin -> dragon (so the
-- tightened CHECK validates), and restore the original 8-species CHECK.
-- The NULL-backfill is NOT reversible (prior NULLs are unknown) — left as-is.
-- =============================================================================

BEGIN;

ALTER TABLE familiar_instances ALTER COLUMN species DROP DEFAULT;

ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY;
UPDATE familiar_instances SET species = 'dragon' WHERE species = 'penguin';
ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;

DO $$
DECLARE cname text;
BEGIN
  SELECT conname INTO cname FROM pg_constraint
   WHERE conrelid = 'familiar_instances'::regclass AND contype = 'c'
     AND pg_get_constraintdef(oid) ILIKE '%species%'
     AND pg_get_constraintdef(oid) ILIKE '%dragon%';
  IF cname IS NOT NULL THEN
    EXECUTE format('ALTER TABLE familiar_instances DROP CONSTRAINT %I', cname);
  END IF;
END $$;

ALTER TABLE familiar_instances
  ADD CONSTRAINT familiar_instances_species_check
  CHECK (species IS NULL OR species IN
    ('owl','fox','cat','dragon','phoenix','turtle','wolf','raven'));

COMMIT;

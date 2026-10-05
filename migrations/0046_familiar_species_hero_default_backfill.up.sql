-- =============================================================================
-- chora-consumption : 0046_familiar_species_hero_default_backfill.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar holistic redesign WS2 (render path), 2026-06-28.
--            docs/FAMILIAR-HOLISTIC-REDESIGN-2026-06-28.md
--
-- Why: the legacy `POST /v1/me/familiars` create path inserts NO species
-- (familiar_instances.species defaulted to NULL), while EXP still advances
-- growth_stage. The FE BreedArt then rendered every NULL-species familiar as
-- the dragon fallback ("same dragon at different stages"). This migration:
--   1. allows the 5th hero 'penguin' in the species CHECK (5-hero roster
--      {dragon,phoenix,owl,fox,penguin} per the 2026-06-28 redesign);
--   2. gives `species` a random-hero DEFAULT so NEW legacy-path familiars
--      render a real creature (interim only — the odds-weighted gacha roll
--      remains the egg-hatch flow growth.HatchEgg, which sets species at hatch);
--   3. backfills EXISTING null-species rows to a deterministic hero spread.
--
-- Never overwrites an already-rolled species (WHERE species IS NULL only).
-- =============================================================================

BEGIN;

-- 1) Permit 'penguin' in the species CHECK. Drop the existing species CHECK by
--    discovered name (rename-safe + idempotent), then re-add including penguin.
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
    ('owl','fox','cat','dragon','phoenix','turtle','wolf','raven','penguin'));

-- 2) Interim DEFAULT: new legacy-path familiars get a random hero so they render
--    a real creature until the egg-hatch flow owns the (odds-weighted) roll.
ALTER TABLE familiar_instances
  ALTER COLUMN species SET DEFAULT
    (ARRAY['dragon','phoenix','owl','fox','penguin'])[1 + floor(random() * 5)::int];

-- 3) Backfill existing NULL-species rows -> deterministic hero spread. Owner-run
--    cross-tenant one-time data fix: toggle FORCE RLS off for the UPDATE then
--    restore immediately (atomic within this transaction).
ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY;
UPDATE familiar_instances
   SET species = (ARRAY['dragon','phoenix','owl','fox','penguin'])
                   [1 + (abs(hashtext(familiar_id::text)) % 5)],
       species_rarity = COALESCE(species_rarity, 'common')
 WHERE deleted_at IS NULL
   AND species IS NULL;
ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;

COMMIT;

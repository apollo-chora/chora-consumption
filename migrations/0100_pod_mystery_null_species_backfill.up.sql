-- =============================================================================
-- chora-consumption : 0100_pod_mystery_null_species_backfill.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Story    : CHO-2227
-- Context  : "A pod is a mystery artifact" (owner rule, 2026-07-16) +
--            docs/design/companion-art-brief.md §3 ("the type is rolled at the
--            moment of opening, so the Pod must NOT hint at any species").
--
-- Why: 0032:36-38 declared the invariant — "Breed/species (lootbox-rolled at
-- HatchEgg, NOT at purchase). NULL while in Stage 0 (Egg); set permanently at
-- hatching." 0046 then gave `species` a random-hero column DEFAULT, and
-- provisionEggInsertSQL omitted the column, so EVERY pod sold since has carried
-- a hidden random species. That decoy is read by ownerCommittedSpeciesSQL and
-- silently becomes the account's PERMANENT committed species — a breed rolled
-- by nobody, shown to no one, and concealed by the incubation card.
-- Live evidence at authoring time: two freshly-provisioned pods came back
-- "fox" and "penguin"; dale's pod 62b35250 holds "dragon" while his committed
-- species is Fox.
--
-- The code fix (naming `species` NULL in provisionEggInsertSQL) stops NEW pods.
-- This repairs the ones already sold, restoring 0032's invariant.
--
-- SCOPE — deliberately narrow. Only UNOPENED pods:
--   growth_stage = 0  AND  hatched_at IS NULL  AND  deleted_at IS NULL
-- A hatched companion's species is its identity and is NEVER touched. This is
-- additive-safe and idempotent (re-running sets NULL to NULL).
--
-- ⚠ The 0046 DEFAULT is deliberately LEFT IN PLACE. Its stated purpose (legacy
-- render art) is dead, but it has a live consumer: POST /v1/me/familiars/acquire
-- WITHOUT a species pick relies on the INSERT roll standing
-- (born_hatched.go: "the pick (or roll) stands"). Dropping it would leave an
-- acquired companion at stage 1 with no species → the Pod renders for a hatched
-- companion. Retiring it needs InitBornHatched to own an explicit roll first.
-- =============================================================================

BEGIN;

-- Cross-tenant owner-run one-time data repair: FORCE RLS is enabled on this
-- table, so toggle it off for the UPDATE and restore it immediately — atomic
-- within this transaction (mirrors the 0046:53-60 pattern).
ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY;

UPDATE familiar_instances
   SET species = NULL
 WHERE growth_stage = 0
   AND hatched_at IS NULL
   AND deleted_at IS NULL
   AND species IS NOT NULL;

ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;

COMMIT;

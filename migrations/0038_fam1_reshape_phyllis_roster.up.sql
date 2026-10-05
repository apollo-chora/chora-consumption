-- =============================================================================
-- chora-consumption : 0038_fam1_reshape_phyllis_roster.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption (ONLY — cross-DB queries forbidden)
-- Author        : E2E-BE-FAM-1 close (2026-05-16)
-- Architecture  : docs/architecture/adrs/adr-149-familiar-growth-stage-model.md
--                 docs/m13/e2e-fe-coord-directive-2026-05-16.md:185 (FAM-1 row)
-- Story         : E2E-BE-FAM-1 — reshape /a/familiar demo roster (P2)
--
-- Purpose:
--   FE side (compiled bundle at chora.site, ack 8661f49a) is deploy-ready
--   with 3 new BreedArtComponent renderings:
--     - (species=phoenix, stage=1) → Baby Phoenix art
--     - (any species, stage=0)     → shared Mystery Egg art (no breed-pending watermark)
--     - (species=dragon, stage=4)  → Teenage Dragon art
--
--   Pre-reshape, Phyllis's roster from prior seed bundles was:
--     - Pythagoras  (...f101, owl,     Stage 1)
--     - Kepler      (...f102, fox,     Stage 3)
--     - Galileo     (...f103, dragon,  Stage 5)
--     - Eira        (...e1a0, dragon,  Stage 2) — from 0036_phyllis_eira_seed
--
--   None of those rows hit the 3 new asset cells. Result: blank rendering on
--   /a/familiar (FE falls back to breed-pending watermark per BreedArtComponent
--   §1 — "demo ships Dragon-only").
--
--   Reshape per coordinator directive:
--     1) Aria         (...f1a1, phoenix, Stage 1) — hits Baby Phoenix
--     2) Mystery Egg  (...f1a2, dragon,  Stage 0) — hits shared Mystery Egg
--     3) Ignis        (...f1a3, dragon,  Stage 4) — hits Teenage Dragon
--     4) Eira         (...e1a0, dragon,  Stage 2) — UNCHANGED (stock Drakeling)
--
--   The old rows f101/f102/f103 are SOFT-DELETED (deleted_at = now()) per
--   .claude/rules/ddd-enforcement.md §Soft Deletes. The ListRosterByOwner
--   query at services/chora-consumption/internal/adapter/repo/pg/familiar_instance.go:108-109
--   filters `WHERE deleted_at IS NULL`, so soft-deleted rows do NOT appear
--   in the BFF `GET /v1/me/familiars` response.
--
-- Canonical NEW familiars:
--
--   Aria (Baby Phoenix):
--     familiar_id  : 00000000-0000-7000-8000-00000000f1a1
--                     (mnemonic `f1a1` for FAM-1 Aria)
--     tenant_id    : 11111111-1111-7111-8111-111111111111  (MTM tenant)
--     owner_gcid   : 00000000-0000-7000-8000-000000001999  (Phyllis GCID)
--     name         : Aria
--     species      : phoenix
--     growth_stage : 1   (Baby / Hatchling per ADR-149 stage ladder)
--     species_rarity   : uncommon  (phoenix lootbox weight in Standard egg)
--     rolled_probability : 8.00
--     growth_exp   : 150 (matches Stage 1 lower threshold; mirrors Pythagoras)
--
--   Mystery Egg:
--     familiar_id  : 00000000-0000-7000-8000-00000000f1a2
--     name         : Mystery Egg
--     species      : dragon    (asset is in /assets/familiars/dragon/ but FE
--                              renders shared egg art for ANY species @ stage 0)
--     growth_stage : 0  (Egg — pre-hatch state)
--     growth_exp   : 0
--     hatched_at   : NULL (egg, not yet hatched)
--     species_rarity / rolled_probability / persona_summary : NULL (will be
--       resolved at the lootbox HatchEgg call — placeholder pre-hatch)
--
--   Ignis (Teenage Dragon):
--     familiar_id  : 00000000-0000-7000-8000-00000000f1a3
--     name         : Ignis
--     species      : dragon
--     growth_stage : 4  (Structural per ADR-149 stage ladder — pre-Teen)
--     species_rarity : common
--     rolled_probability : 22.00
--     growth_exp   : 9500  (rough mid-range cumulative across atom_session +
--                          ebbinghaus + hex_expand + conv_turn — story-only,
--                          IMDA D2 audit ignores synthetic ledger rows below)
--
-- HARD RULE: cross-database queries forbidden (per .claude/rules/ddd-enforcement
-- HARD RULE). resonant_atom_id is a UUID-only reference into chora_creation.
-- No FK; validation deferred to chora-creation.ValidateAtomID.
--
-- Resilience properties (per feedback_resilience_priority + agentic-resilience-d6):
--   - BEGIN/COMMIT wrapping (rolls back on partial failure)
--   - Soft-delete UPDATE uses COALESCE(deleted_at, now()) so re-runs are idempotent
--     (a row already soft-deleted keeps its original deleted_at timestamp)
--   - ON CONFLICT (familiar_id) DO NOTHING on the 3 new INSERTs
--   - chora.tenant_id GUC SET LOCAL so FORCE ROW LEVEL SECURITY policies
--     resolve for the migrate role (which OWNS the table but is NOT BYPASSRLS).
--   - Dead-pod resume: re-running converges to the same end state
--   - Trigger-disable workaround inherited from 0036_phyllis_eira_seed.up.sql
--     (trg_familiar_instances_updated_at fires consumption_set_updated_at()
--     which expects a `version` column that familiar_instances lacks; full
--     fix is owned by the per-service migration paydown track)
-- =============================================================================

BEGIN;

SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
SET LOCAL chora.user_gcid = '00000000-0000-7000-8000-000000001999';
SET LOCAL chora.role      = 'learner';

-- -----------------------------------------------------------------------------
-- Trigger-disable workaround (mirrors 0036_phyllis_eira_seed.up.sql §85).
-- The trg_familiar_instances_updated_at trigger calls
-- consumption_set_updated_at() which expects a `version` column on every table
-- it fires on; familiar_instances has none. The full fix is owned by the
-- migration paydown track. For this seed we disable + re-enable inside the
-- same TX so a rollback restores the original state automatically.
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_instances DISABLE TRIGGER trg_familiar_instances_updated_at;

-- -----------------------------------------------------------------------------
-- 1) Soft-delete the 3 pre-FAM-1 familiars (Pythagoras / Kepler / Galileo).
--
-- COALESCE(deleted_at, now()) means: if already soft-deleted, keep the prior
-- timestamp (idempotent). If not, set to now().
--
-- ListRosterByOwner filters deleted_at IS NULL → these rows drop out of the
-- BFF response. RLS still honors them; tenancy invariants preserved.
-- -----------------------------------------------------------------------------
UPDATE familiar_instances
   SET deleted_at = COALESCE(deleted_at, now())
 WHERE familiar_id IN (
       '00000000-0000-7000-8000-00000000f101'::uuid,  -- Pythagoras
       '00000000-0000-7000-8000-00000000f102'::uuid,  -- Kepler
       '00000000-0000-7000-8000-00000000f103'::uuid   -- Galileo
       );

-- -----------------------------------------------------------------------------
-- 2) familiar_instances — Aria (Phoenix, Stage 1 Baby/Hatchling)
--
-- Idempotent on (familiar_id) — re-runs are no-ops.
-- -----------------------------------------------------------------------------
INSERT INTO familiar_instances (
    familiar_id,
    tenant_id,
    owner_gcid,
    name,
    specialization,
    evolution_tier,
    skill_slots_unlocked,
    memory_context_capacity,
    persona_summary,
    configured_rules,
    growth_stage,
    species,
    shiny_variant,
    species_rarity,
    rolled_probability,
    growth_exp,
    resonant_atom_id,
    visible_kg_neighbors,
    hatched_at,
    aha_moment_consumed,
    aha_moment_active_until,
    aha_moment_preview_llm_tier,
    egg_sku,
    egg_purchase_id,
    egg_purchased_at,
    egg_soft_expiry_at,
    egg_hard_expiry_at,
    egg_source,
    effective_llm_tier_cached,
    last_stage_up_at,
    deleted_at
) VALUES (
    '00000000-0000-7000-8000-00000000f1a1'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'Aria',
    'music',
    'apprentice',
    1,
    1000,
    'Phoenix Familiar bonded to Phyllis at the Hatchling stage. Specialised on rhythm, melody, breath — and rising on the page after every reset. Warm, encouraging, song-shaped.',
    '{"max_hint_count": 3, "max_session_minutes": 25}'::jsonb,
    1,                        -- growth_stage = Baby / Hatchling
    'phoenix',
    FALSE,
    'uncommon',               -- phoenix is uncommon vs dragon/common
    8.00,                     -- phoenix weight in egg.standard.v1 distribution
    150,                      -- Stage 1 lower threshold (mirrors Pythagoras)
    '00000000-0000-7000-8000-00000000a0a1'::uuid,
    ARRAY[]::uuid[],
    '2026-05-16T00:00:00Z'::timestamptz,
    FALSE,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL, NULL,  -- legacy bonded; egg provenance NULL
    'flash-lite',
    '2026-05-16T00:00:00Z'::timestamptz,
    NULL
)
ON CONFLICT (familiar_id) DO NOTHING;

-- -----------------------------------------------------------------------------
-- 3) familiar_instances — Mystery Egg (Dragon, Stage 0 / pre-hatch)
--
-- Stage 0 is the Egg state per ADR-149 stage ladder. FE renders the shared
-- /assets/familiars/dragon/dragon-stage-0.png art (mystery egg shape — species
-- "dragon" is the directory home but the asset is canonically species-agnostic
-- per the directive at docs/m13/e2e-fe-coord-directive-2026-05-16.md:185).
--
-- Pre-hatch invariants:
--   - growth_exp = 0
--   - hatched_at = NULL
--   - species_rarity = NULL (resolved at HatchEgg lootbox call)
--   - rolled_probability = NULL
-- -----------------------------------------------------------------------------
INSERT INTO familiar_instances (
    familiar_id,
    tenant_id,
    owner_gcid,
    name,
    specialization,
    evolution_tier,
    skill_slots_unlocked,
    memory_context_capacity,
    persona_summary,
    configured_rules,
    growth_stage,
    species,
    shiny_variant,
    species_rarity,
    rolled_probability,
    growth_exp,
    resonant_atom_id,
    visible_kg_neighbors,
    hatched_at,
    aha_moment_consumed,
    aha_moment_active_until,
    aha_moment_preview_llm_tier,
    egg_sku,
    egg_purchase_id,
    egg_purchased_at,
    egg_soft_expiry_at,
    egg_hard_expiry_at,
    egg_source,
    effective_llm_tier_cached,
    last_stage_up_at,
    deleted_at
) VALUES (
    '00000000-0000-7000-8000-00000000f1a2'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'Mystery Egg',
    'unbound',
    'apprentice',
    0,                         -- skill_slots locked pre-hatch
    1000,
    'A warm, humming egg waiting to choose Phyllis back. Tap to hatch.',
    '{"max_hint_count": 0, "max_session_minutes": 0}'::jsonb,
    0,                         -- growth_stage = Egg
    'dragon',                  -- asset lives in /dragon/ but FE renders shared egg
    FALSE,
    NULL,                      -- rarity not yet rolled
    NULL,                      -- rolled_probability not yet sampled
    0,                         -- no EXP — pre-hatch
    NULL,                      -- resonant_atom_id resolved post-hatch
    ARRAY[]::uuid[],
    NULL,                      -- hatched_at NULL (egg, pre-hatch)
    FALSE,
    NULL,
    NULL,
    'egg.standard.v1',
    NULL,                      -- egg_purchase_id NULL (synthetic demo egg, not Stripe-backed)
    '2026-05-15T00:00:00Z'::timestamptz,
    '2026-06-14T00:00:00Z'::timestamptz,
    '2026-07-14T00:00:00Z'::timestamptz,
    'tenant_grant',            -- synthesised demo egg (not Stripe 'purchase')
    NULL,                      -- no LLM tier resolved pre-hatch
    NULL,
    NULL
)
ON CONFLICT (familiar_id) DO NOTHING;

-- -----------------------------------------------------------------------------
-- 4) familiar_instances — Ignis (Dragon, Stage 4 Structural / pre-Teen)
--
-- Note on stage semantics: ADR-149 ladder is 0=Egg, 1=Baby, 2=Fledgling,
-- 3=Angelic, 4=Structural, 5=Teen Dragon, 6=Matured. The FE-side directive
-- says "(species=dragon, stage=4) → Teenage Dragon" — meaning the new art
-- file dragon-stage-4.png is the visual that the FE will render at stage=4.
-- The ADR-149 internal label (Structural) differs from the asset filename
-- caption (Teenage), but the canonical column value is integer 4 either way.
-- -----------------------------------------------------------------------------
INSERT INTO familiar_instances (
    familiar_id,
    tenant_id,
    owner_gcid,
    name,
    specialization,
    evolution_tier,
    skill_slots_unlocked,
    memory_context_capacity,
    persona_summary,
    configured_rules,
    growth_stage,
    species,
    shiny_variant,
    species_rarity,
    rolled_probability,
    growth_exp,
    resonant_atom_id,
    visible_kg_neighbors,
    hatched_at,
    aha_moment_consumed,
    aha_moment_active_until,
    aha_moment_preview_llm_tier,
    egg_sku,
    egg_purchase_id,
    egg_purchased_at,
    egg_soft_expiry_at,
    egg_hard_expiry_at,
    egg_source,
    effective_llm_tier_cached,
    last_stage_up_at,
    deleted_at
) VALUES (
    '00000000-0000-7000-8000-00000000f1a3'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'Ignis',
    'physics',
    'adept',
    4,
    8000,
    'Dragon Familiar — late-Structural / pre-Teen. Burns through first-principles dialogue: forces, energy, equilibrium. Direct, Socratic, occasionally smug.',
    '{"max_hint_count": 4, "max_session_minutes": 35}'::jsonb,
    4,                         -- growth_stage = Structural (pre-Teen)
    'dragon',
    FALSE,
    'common',
    25.00,                     -- dragon Standard-egg weight
    9500,                      -- mid-range cumulative EXP (story-only)
    '00000000-0000-7000-8000-00000000a0a3'::uuid,
    ARRAY[
        '00000000-0000-7000-8000-00000000a0a1'::uuid,
        '00000000-0000-7000-8000-00000000a0a2'::uuid
    ]::uuid[],
    '2026-05-01T00:00:00Z'::timestamptz,
    FALSE,
    NULL,
    NULL,
    NULL, NULL, NULL, NULL, NULL, NULL,  -- legacy bonded; egg provenance NULL
    'flash',
    '2026-05-15T00:00:00Z'::timestamptz,
    NULL
)
ON CONFLICT (familiar_id) DO NOTHING;

-- Re-enable the trigger we disabled at the head of the transaction.
ALTER TABLE familiar_instances ENABLE TRIGGER trg_familiar_instances_updated_at;

COMMIT;

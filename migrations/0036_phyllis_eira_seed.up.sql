-- =============================================================================
-- chora-consumption : 0036_phyllis_eira_seed.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption (ONLY — cross-DB queries forbidden)
-- Author        : Phyllis demo seed close (2026-05-15)
-- Architecture  : docs/architecture/adrs/adr-149-familiar-growth-stage-model.md
--                 docs/architecture/adrs/adr-154-familiar-chat-surface.md
--                 services/chora-consumption/migrations/0032_familiar_growth.sql
-- Story         : CHO-1496 (Platform Infra | Phyllis Demo | Idempotent Seed Fixtures)
--                 plus the round-13 FE/BE handoff Eira UUIDv7 reconciliation.
--
-- Purpose:
--   Seed Phyllis's canonical Familiar — Eira (Dragon, Stage 2 Fledgling) —
--   directly via a migration so the BE `GET /v1/me/familiars` route returns a
--   non-empty `items[]` for Phyllis's GCID. Pre-seed state: chora_consumption
--   had Pythagoras (owl S1) / Kepler (fox S3) / Galileo (dragon S5) for Phyllis
--   from the Cloud-Run-Job-driven seed bundle, but NO Eira. The FE mock
--   fallback in familiar-growth.service.ts was carrying the Eira identity in
--   the absence of a BE row; round-13 of the FE/BE handoff established that
--   the placeholder must move into the DB.
--
-- Canonical Eira fixture (per FE round-13 ack 5f9d21a5 → coordinator
-- direction 2026-05-15):
--
--   familiar_id          : 00000000-0000-7000-8000-00000000e1a0
--                           (valid RFC 4122 UUIDv7; mnemonic `e1a0` for Eira)
--   tenant_id            : 11111111-1111-7111-8111-111111111111  (MTM tenant)
--   owner_gcid           : 00000000-0000-7000-8000-000000001999  (Phyllis GCID)
--   name                 : Eira
--   species              : dragon
--   growth_stage         : 2  (Fledgling per ADR-149 stage ladder)
--   evolution_tier       : apprentice
--   growth_exp           : 200  (matches FE `expCurrent`/`expNextThreshold`
--                           transition point; gets her into Stage 2)
--   resonant_atom_id     : 00000000-0000-7000-8000-00000000a0a2
--                           (A+ MCQ atom = canonical demo atom with seeded Q)
--   shiny_variant        : FALSE
--   species_rarity       : common
--   rolled_probability   : 25.00  (matches Standard egg dragon weight per
--                           chora_tenancy.familiar_egg_purchases.breed_distribution)
--   hatched_at           : 2026-05-13T00:00:00Z  (matches Phyllis-demo seed
--                           cohort timestamps in 07a_consumption_growth.sql)
--   effective_llm_tier_cached: flash-lite  (matches FE mockEiraState)
--   egg_*                : NULL  (legacy bonded Familiar — pre-ADR-149 era
--                           wiring; the user-facing demo doesn't transit egg)
--
-- FE Phase J + future tests should reference this exact UUID; the FE mock
-- fallback drops out once this row is queryable. See:
--   chora-web/src/app/core/familiar/familiar-growth.service.ts §mockEiraState
--   docs/m13/handoff-fe-to-be-service-2026-05-14.md round-15
--
-- HARD RULE: cross-database queries forbidden (per .claude/rules/ddd-enforcement
-- HARD RULE). resonant_atom_id is a UUID-only reference into chora_creation.
-- No FK; validation deferred to chora-creation.ValidateAtomID (M13.A wiring).
--
-- Resilience properties (per feedback_resilience_priority + agentic-resilience-d6):
--   - BEGIN/COMMIT wrapping (rolls back on partial failure)
--   - ON CONFLICT (familiar_id) DO NOTHING on the familiar row
--   - ON CONFLICT (familiar_id, source, idempotency_key) DO NOTHING on EXP rows
--   - ON CONFLICT (tenant_id, familiar_id, source, day_bucket) DO NOTHING on counters
--   - chora.tenant_id GUC SET LOCAL so FORCE ROW LEVEL SECURITY policies
--     resolve for the migrate role (which OWNS the table but is NOT BYPASSRLS).
--   - Dead-pod resume: re-running converges to the same end state
--   - Trigger-disable workaround inherited from 07a_consumption_growth.sql
--     (trg_familiar_instances_updated_at fires consumption_set_updated_at()
--     which expects a `version` column that familiar_instances lacks; full
--     fix is owned by the per-service migration paydown track)
-- =============================================================================

BEGIN;

SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
SET LOCAL chora.user_gcid = '00000000-0000-7000-8000-000000001999';
SET LOCAL chora.role      = 'learner';

-- -----------------------------------------------------------------------------
-- Trigger-disable workaround (mirrors 07a_consumption_growth.sql lines 42-59).
-- The trg_familiar_instances_updated_at trigger calls
-- consumption_set_updated_at() which expects a `version` column on every table
-- it fires on; familiar_instances has none. The full fix is owned by the
-- migration paydown track. For this seed we disable + re-enable inside the
-- same TX so a rollback restores the original state automatically.
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_instances DISABLE TRIGGER trg_familiar_instances_updated_at;

-- -----------------------------------------------------------------------------
-- 1) familiar_instances — Eira (Dragon, Stage 2 Fledgling)
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
    -- ADR-149 growth axis
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
    -- Egg provenance — legacy bonded Familiar (pre-ADR-149 era wiring); NULL
    -- per ADR-149 §backfill.
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
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'Eira',
    'cspo',
    'apprentice',
    1,
    1000,
    'Dragon Familiar bonded to Phyllis at the Fledgling stage. Specialised on Certified Scrum Product Owner content. Curious, encouraging, cite-first.',
    '{"max_hint_count": 3, "max_session_minutes": 25}'::jsonb,
    -- ADR-149 growth axis
    2,                        -- growth_stage = Fledgling
    'dragon',
    FALSE,
    'common',
    25.00,
    200,                      -- growth_exp at Stage 2 lower threshold
    '00000000-0000-7000-8000-00000000a0a2'::uuid,
    ARRAY[]::uuid[],
    '2026-05-13T00:00:00Z'::timestamptz,
    FALSE,
    NULL,
    NULL,
    -- Egg provenance — legacy bonded; NULL
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    NULL,
    'flash-lite',
    '2026-05-13T00:00:00Z'::timestamptz,
    NULL
)
ON CONFLICT (familiar_id) DO NOTHING;

-- -----------------------------------------------------------------------------
-- 2) familiar_growth_events — minimal EXP ledger backing Eira's 200 EXP cache
--
-- Guarded: familiar_growth_events ships in 0032_familiar_growth.sql. If the
-- migration has not been applied (fresh DB), skip with a NOTICE so the seed
-- degrades cleanly rather than aborting.
--
-- Ledger composition (sum = 200 EXP = matches growth_exp above):
--   a) hatch_roll        —   0 EXP (audit-only lootbox roll record)
--   b) atom_session      —  50 EXP (Phyllis's first atom completion)
--   c) ebbinghaus_review —  30 EXP (Day-2 retention reinforce)
--   d) hex_expand        —  20 EXP (KG hexagon expansion)
--   e) conv_turn         — 100 EXP (productive Familiar chat run; 2 turns x 50)
--
-- All event_ids are deterministic 'e1a0e0NN' suffix for re-runnability.
-- idempotency_key uses '{source}:eira-seed' so re-runs collide and DO NOTHING
-- per the (familiar_id, source, idempotency_key) UNIQUE constraint in 0032.
-- -----------------------------------------------------------------------------
DO $eira_exp$
BEGIN
IF NOT EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema='public' AND table_name='familiar_growth_events'
) THEN
    RAISE NOTICE '0036: familiar_growth_events missing (0032 not applied) — skipping EXP ledger seed';
    RETURN;
END IF;

INSERT INTO familiar_growth_events (
    growth_event_id, tenant_id, familiar_id, owner_gcid,
    source, requested_delta, awarded_delta, exp_total_after,
    daily_cap_hit, triggered_stage_up, triggered_revelation,
    idempotency_key,
    source_event_id, source_topic, source_session_id, source_turn_seq,
    awarded_at
) VALUES
-- a) hatch_roll — audit-only (delta 0); records lootbox dragon roll
(
    '00000000-0000-7000-8000-0000e1a0e001'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'hatch_roll', 0, 0, 0,
    FALSE, FALSE, FALSE,
    'hatch_roll:eira-seed',
    NULL, 'chora.consumption.familiar.egg_hatched.v1', NULL, NULL,
    '2026-05-13T00:00:00Z'::timestamptz
),
-- b) atom_session — first A+ atom completion
(
    '00000000-0000-7000-8000-0000e1a0e002'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'atom_session', 50, 50, 50,
    FALSE, FALSE, FALSE,
    'atom_session:eira-seed',
    NULL, 'chora.consumption.atom_session.completed.v1', NULL, NULL,
    '2026-05-13T00:30:00Z'::timestamptz
),
-- c) ebbinghaus_review — Day-2 retention reinforce
(
    '00000000-0000-7000-8000-0000e1a0e003'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'ebbinghaus_review', 30, 30, 80,
    FALSE, FALSE, FALSE,
    'ebbinghaus_review:eira-seed',
    NULL, 'chora.consumption.review.completed.v1', NULL, NULL,
    '2026-05-13T01:00:00Z'::timestamptz
),
-- d) hex_expand — KG hexagon expansion
(
    '00000000-0000-7000-8000-0000e1a0e004'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'hex_expand', 20, 20, 100,
    FALSE, FALSE, FALSE,
    'hex_expand:eira-seed',
    NULL, 'chora.consumption.kg.hexagon_expanded.v1', NULL, NULL,
    '2026-05-13T01:30:00Z'::timestamptz
),
-- e) conv_turn — productive Familiar chat (2 turns @ 50 EXP each)
(
    '00000000-0000-7000-8000-0000e1a0e005'::uuid,
    '11111111-1111-7111-8111-111111111111'::uuid,
    '00000000-0000-7000-8000-00000000e1a0'::uuid,
    '00000000-0000-7000-8000-000000001999'::uuid,
    'conv_turn', 100, 100, 200,
    FALSE, FALSE, FALSE,
    'conv_turn:eira-seed',
    NULL, NULL, NULL, NULL,
    '2026-05-13T02:00:00Z'::timestamptz
)
ON CONFLICT (familiar_id, source, idempotency_key) DO NOTHING;

-- -----------------------------------------------------------------------------
-- 3) familiar_growth_daily_counters — per-day per-source cap state
-- -----------------------------------------------------------------------------
INSERT INTO familiar_growth_daily_counters (
    tenant_id, familiar_id, source, day_bucket, exp_awarded, event_count, last_awarded_at
) VALUES
('11111111-1111-7111-8111-111111111111'::uuid, '00000000-0000-7000-8000-00000000e1a0'::uuid,
    'hatch_roll',        DATE '2026-05-13',   0, 1, '2026-05-13T00:00:00Z'::timestamptz),
('11111111-1111-7111-8111-111111111111'::uuid, '00000000-0000-7000-8000-00000000e1a0'::uuid,
    'atom_session',      DATE '2026-05-13',  50, 1, '2026-05-13T00:30:00Z'::timestamptz),
('11111111-1111-7111-8111-111111111111'::uuid, '00000000-0000-7000-8000-00000000e1a0'::uuid,
    'ebbinghaus_review', DATE '2026-05-13',  30, 1, '2026-05-13T01:00:00Z'::timestamptz),
('11111111-1111-7111-8111-111111111111'::uuid, '00000000-0000-7000-8000-00000000e1a0'::uuid,
    'hex_expand',        DATE '2026-05-13',  20, 1, '2026-05-13T01:30:00Z'::timestamptz),
('11111111-1111-7111-8111-111111111111'::uuid, '00000000-0000-7000-8000-00000000e1a0'::uuid,
    'conv_turn',         DATE '2026-05-13', 100, 1, '2026-05-13T02:00:00Z'::timestamptz)
ON CONFLICT (tenant_id, familiar_id, source, day_bucket) DO NOTHING;

END $eira_exp$;

-- Re-enable the trigger we disabled at the head of the transaction.
ALTER TABLE familiar_instances ENABLE TRIGGER trg_familiar_instances_updated_at;

COMMIT;

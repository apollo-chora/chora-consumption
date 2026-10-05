-- =============================================================================
-- chora-consumption : 0033_familiar_growth_egg_purchase_unique.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : Iter G.5 PROD-B (2026-05-13)
-- Architecture  : docs/architecture/adrs/adr-149-familiar-growth-stage-model.md
--                 follow-on to 0032_familiar_growth.sql
--
-- Closes the ProvisionEgg idempotency contract by adding a UNIQUE constraint
-- on familiar_instances.egg_purchase_id. The Iter G.5 pg.GrowthRepo.ProvisionEgg
-- adapter relies on this constraint to make Stripe webhook redelivery a no-op:
--
--     INSERT ... ON CONFLICT (egg_purchase_id) DO NOTHING
--     SELECT * FROM familiar_instances WHERE egg_purchase_id = $1
--
-- Per ADR-149: egg_purchase_id is 1:1 with chora_tenancy.familiar_egg_purchases.
-- The 0032 migration declared this intent in the column comment but did NOT
-- enforce it at the schema level. This migration closes that gap.
--
-- APPROVAL-GATED: requires explicit user approval to apply against Cloud SQL.
-- =============================================================================

BEGIN;

-- Partial unique index — only non-NULL egg_purchase_id values are enforced.
-- Legacy bonded Familiars from pre-ADR-149 era have NULL here and are exempt
-- per the 0032 migration comment.
CREATE UNIQUE INDEX IF NOT EXISTS uq_familiar_instances_egg_purchase_id
    ON familiar_instances (egg_purchase_id)
    WHERE egg_purchase_id IS NOT NULL;

COMMENT ON INDEX uq_familiar_instances_egg_purchase_id IS
    'ADR-149 Iter G.5 PROD-B: enforces 1:1 (egg_purchase_id ↔ familiar_instances) for Stripe webhook idempotency. Partial — NULL values exempt.';

COMMIT;

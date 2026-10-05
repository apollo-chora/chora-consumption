-- =============================================================================
-- chora-consumption : 0032_familiar_growth.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : Iter G.1 — Familiar Growth Stage Model (2026-05-13)
-- Architecture  : docs/architecture/adrs/adr-149-familiar-growth-stage-model.md
--                 chora-contracts/proto/events/consumption/familiar.proto §ADR-149
--                 chora-contracts/proto/services/consumption/v1/familiar_growth_service.proto
--
-- Introduces the 7-stage Angelic Dragon growth axis (per ADR-149) on top
-- of the existing familiar_instances table from 0006_familiar_multi.sql.
--
-- Approach: ADDITIVE — no destructive schema changes. Existing age_stage
-- (stored in configured_rules JSONB by Iter 1) remains for backwards
-- compatibility during the Iter G.2 cutover window; ADR-149 §backfill
-- handles the value mapping at app level.
--
-- Tables touched:
--   1. familiar_instances     — ADD columns (additive)
--   2. familiar_growth_events — NEW append-only EXP ledger
--   3. familiar_growth_daily_counters — NEW (per-day per-source cap tracking)
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- familiar_instances — ADD growth_stage axis columns (additive)
-- -----------------------------------------------------------------------------
ALTER TABLE familiar_instances
    -- 7-stage axis per ADR-149 (0 = Egg, 6 = Matured). New rows start at 0.
    -- Stage names are breed-neutral: egg | baby | fledgling | awakened |
    -- structural | teen | matured. Per-breed UI nickname composed at display.
    ADD COLUMN IF NOT EXISTS growth_stage SMALLINT NOT NULL DEFAULT 0
        CHECK (growth_stage BETWEEN 0 AND 6),

    -- Breed/species (lootbox-rolled at HatchEgg, NOT at purchase). NULL while
    -- in Stage 0 (Egg); set permanently at hatching. Mirrors the canonical
    -- FamiliarSpecies enum in chora-contracts/proto/events/consumption/familiar.proto.
    ADD COLUMN IF NOT EXISTS species TEXT
        CHECK (species IN ('owl','fox','cat','dragon','phoenix','turtle','wolf','raven')
            OR species IS NULL),
    ADD COLUMN IF NOT EXISTS shiny_variant BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS species_rarity TEXT
        CHECK (species_rarity IN ('common','uncommon','rare','legendary')
            OR species_rarity IS NULL),
    -- Probability the SKU assigned to this species at the roll time (IMDA
    -- D2 transparency audit trail).
    ADD COLUMN IF NOT EXISTS rolled_probability NUMERIC(5,2)
        CHECK (rolled_probability IS NULL
            OR (rolled_probability >= 0 AND rolled_probability <= 100)),

    -- Cumulative EXP since hatching. Source-of-truth for current stage is
    -- the threshold lookup; this column is denormalized cache for fast read.
    ADD COLUMN IF NOT EXISTS growth_exp INTEGER NOT NULL DEFAULT 0
        CHECK (growth_exp >= 0),

    -- Immutable Resonant Atom anchor (UUIDv7 of a LearningAtom). NULL while
    -- in Stage 0 (Egg); set permanently at HatchEgg time. Cross-DB ref to
    -- chora_creation.atoms — validated via chora-creation.ValidateAtomID
    -- at HatchEgg, not at insert (no FK).
    ADD COLUMN IF NOT EXISTS resonant_atom_id UUID,

    -- KG neighbors visible to this Familiar's RAG slice. Length bounded by
    -- the current stage (Stage 2 = 1, Stage 3 = 2, Stage 4 = 4, ..., Stage 6
    -- = unlimited — app-level enforcement).
    ADD COLUMN IF NOT EXISTS visible_kg_neighbors UUID[] NOT NULL DEFAULT ARRAY[]::UUID[],

    -- Hatching timestamp (Stage 0 → 1 transition). NULL means still in Egg.
    ADD COLUMN IF NOT EXISTS hatched_at TIMESTAMPTZ,

    -- Stage-3 Source Revelation (Aha-moment) state.
    -- aha_moment_consumed = TRUE after the one-shot fires (lifetime).
    -- aha_moment_active_until carries the preview window end timestamp;
    -- NULL outside the window.
    ADD COLUMN IF NOT EXISTS aha_moment_consumed BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS aha_moment_active_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS aha_moment_preview_llm_tier TEXT
        CHECK (aha_moment_preview_llm_tier IN
            ('flash-lite', 'flash', 'flash-reasoning', 'pro')
            OR aha_moment_preview_llm_tier IS NULL),

    -- Egg purchase provenance (NULL for legacy bonded Familiars from
    -- pre-ADR-149 era; populated for all post-ADR-149 rows).
    ADD COLUMN IF NOT EXISTS egg_sku TEXT,
    ADD COLUMN IF NOT EXISTS egg_purchase_id UUID,    -- 1:1 with chora_tenancy.familiar_egg_purchases.purchase_id
    ADD COLUMN IF NOT EXISTS egg_purchased_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS egg_soft_expiry_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS egg_hard_expiry_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS egg_source TEXT
        CHECK (egg_source IN
            ('purchase', 'subscription_inclusion', 'tenant_grant', 'trial')
            OR egg_source IS NULL),

    -- Cached LLM tier last applied at the most recent stage_up. The
    -- agent runtime reads from session.State.mana_tier × growth_stage at
    -- run time; this column is a snapshot for diagnostics + UI display.
    ADD COLUMN IF NOT EXISTS effective_llm_tier_cached TEXT
        CHECK (effective_llm_tier_cached IN
            ('flash-lite', 'flash', 'flash-reasoning', 'pro')
            OR effective_llm_tier_cached IS NULL),

    ADD COLUMN IF NOT EXISTS last_stage_up_at TIMESTAMPTZ;

-- Partial index for stage-up queries (find candidates due for review).
CREATE INDEX IF NOT EXISTS idx_familiar_instances_growth_stage
    ON familiar_instances (tenant_id, growth_stage)
    WHERE deleted_at IS NULL;

-- Partial index for shiny-variant detection at HatchEgg (must check the
-- caller's active roster for existing Familiars of the would-be-rolled
-- species before committing the roll).
CREATE INDEX IF NOT EXISTS idx_familiar_instances_owner_species
    ON familiar_instances (tenant_id, owner_gcid, species)
    WHERE deleted_at IS NULL AND species IS NOT NULL;

-- Partial index for hard-expiry sweeper (finds unhatched eggs past expiry).
CREATE INDEX IF NOT EXISTS idx_familiar_instances_egg_hard_expiry
    ON familiar_instances (egg_hard_expiry_at)
    WHERE growth_stage = 0
      AND hatched_at IS NULL
      AND deleted_at IS NULL
      AND egg_hard_expiry_at IS NOT NULL;

-- Partial index for Aha-moment window-expiry sweeper.
CREATE INDEX IF NOT EXISTS idx_familiar_instances_aha_active
    ON familiar_instances (aha_moment_active_until)
    WHERE aha_moment_active_until IS NOT NULL
      AND deleted_at IS NULL;

COMMENT ON COLUMN familiar_instances.growth_stage IS
    'ADR-149 7-stage Angelic Dragon axis. 0=Egg (pre-hatch), 1=Baby, 2=Fledgling, 3=Angelic (Source), 4=Structural Growth, 5=Teen Dragon, 6=Matured.';

COMMENT ON COLUMN familiar_instances.resonant_atom_id IS
    'ADR-149 immutable Resonant Atom anchor. UUIDv7 of a LearningAtom in chora_creation. Cross-DB ref validated via chora-creation.ValidateAtomID at HatchEgg.';

COMMENT ON COLUMN familiar_instances.aha_moment_consumed IS
    'ADR-149 Stage-3 Source Revelation: TRUE after the one-shot 24h preview has fired. Lifetime flag.';

-- -----------------------------------------------------------------------------
-- familiar_growth_events — append-only EXP ledger
--
-- Source of truth for growth_exp; familiar_instances.growth_exp is denormalized
-- cache reconcilable from this ledger. Idempotent on (familiar_id, source,
-- idempotency_key) to safely re-process replays.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_growth_events (
    growth_event_id     UUID         PRIMARY KEY DEFAULT gen_random_uuid(),  -- UUIDv7 in app layer
    tenant_id           UUID         NOT NULL,
    familiar_id         UUID         NOT NULL,
    owner_gcid          UUID         NOT NULL,

    -- EXP source taxonomy (ADR-149 §EXP economy). Enforced at app layer
    -- against canonical enum; check here as a defensive belt-and-suspenders.
    -- 'hatch_roll' is a non-EXP audit event (delta=0) recording the
    -- lootbox roll outcome for IMDA D2 transparency.
    source              TEXT         NOT NULL
        CHECK (source IN (
            'atom_session',
            'ebbinghaus_review',
            'hex_expand',
            'conv_turn',
            'daily_dose_open',
            'social_share',
            'social_reaction',
            'junction_accepted',
            'atom_authored',
            'admin_grant',
            'hatch_roll'
        )),

    -- Requested delta BEFORE daily-cap clamping (audit trail). May be 0 for
    -- audit-only entries like 'hatch_roll'.
    requested_delta     INTEGER      NOT NULL CHECK (requested_delta >= 0),

    -- Actually awarded delta AFTER clamping (the value applied to running total).
    awarded_delta       INTEGER      NOT NULL CHECK (awarded_delta >= 0),

    -- Running total after this event (computed at insert via app-level
    -- transaction; not a generated column to preserve append-only semantics
    -- under concurrent inserts).
    exp_total_after     INTEGER      NOT NULL CHECK (exp_total_after >= 0),

    -- Cap flags.
    daily_cap_hit       BOOLEAN      NOT NULL DEFAULT FALSE,
    triggered_stage_up  BOOLEAN      NOT NULL DEFAULT FALSE,
    triggered_revelation BOOLEAN     NOT NULL DEFAULT FALSE,

    -- Idempotency. For event-derived awards, the upstream event_id (UUIDv7).
    -- For runtime conv_turn awards, {session_id}:{turn_seq}.
    idempotency_key     TEXT         NOT NULL,

    -- Provenance (for audit + IMDA D1 evidence).
    source_event_id     UUID,
    source_topic        TEXT,
    source_session_id   UUID,
    source_turn_seq     INTEGER,

    awarded_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- Idempotency: (familiar_id, source, idempotency_key) uniquely identifies
    -- a logical award. Retries with the same key yield no-op + duplicate=true
    -- in the gRPC response.
    UNIQUE (familiar_id, source, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_familiar_growth_events_familiar
    ON familiar_growth_events (tenant_id, familiar_id, awarded_at DESC);

CREATE INDEX IF NOT EXISTS idx_familiar_growth_events_owner
    ON familiar_growth_events (tenant_id, owner_gcid, awarded_at DESC);

CREATE INDEX IF NOT EXISTS idx_familiar_growth_events_source_event
    ON familiar_growth_events (source_event_id)
    WHERE source_event_id IS NOT NULL;

ALTER TABLE familiar_growth_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_growth_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_growth_events
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE familiar_growth_events IS
    'ADR-149 EXP ledger. Append-only; idempotent on (familiar_id, source, idempotency_key). Source-of-truth for familiar_instances.growth_exp.';

-- -----------------------------------------------------------------------------
-- familiar_growth_daily_counters — per-day per-source cap tracking
--
-- AwardExp consults this table BEFORE inserting into familiar_growth_events
-- to enforce daily caps server-side. UPSERT pattern:
--   1. Read current count for (familiar_id, source, day_bucket)
--   2. Compute clamped_delta = min(requested_delta, daily_cap - current_count)
--   3. UPSERT counter row + INSERT event row in same TXN
--
-- day_bucket is the UTC date (00:00 UTC) the event was awarded. Per-tenant
-- timezone is a future-concern (ADR-149 §"Out of scope" — daily resets are
-- UTC for MVP).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS familiar_growth_daily_counters (
    tenant_id      UUID         NOT NULL,
    familiar_id    UUID         NOT NULL,
    source         TEXT         NOT NULL,
    day_bucket     DATE         NOT NULL,
    exp_awarded    INTEGER      NOT NULL DEFAULT 0 CHECK (exp_awarded >= 0),
    event_count    INTEGER      NOT NULL DEFAULT 0 CHECK (event_count >= 0),
    last_awarded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, familiar_id, source, day_bucket)
);

CREATE INDEX IF NOT EXISTS idx_familiar_growth_daily_counters_familiar
    ON familiar_growth_daily_counters (familiar_id, day_bucket DESC);

ALTER TABLE familiar_growth_daily_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_growth_daily_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_growth_daily_counters
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE familiar_growth_daily_counters IS
    'ADR-149 per-(familiar, source, day) EXP counter for daily-cap enforcement. UTC day buckets. UPSERT in same TXN as familiar_growth_events INSERT.';

-- -----------------------------------------------------------------------------
-- Grant new tables to app role(s). The 9999_grant_app_roles.sql migration
-- handles the canonical sweep; this migration adds explicit grants so that
-- 0032 is independently re-runnable on fresh databases without 9999.
-- -----------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'chora_consumption_app') THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON familiar_growth_events TO chora_consumption_app';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON familiar_growth_daily_counters TO chora_consumption_app';
    END IF;
END$$;

COMMIT;

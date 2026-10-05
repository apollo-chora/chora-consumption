-- =============================================================================
-- chora-consumption : 0030_gamification.sql
--
-- Consolidates `chora-gamification` legacy migrations into chora-consumption
-- per M12.2 Batch 3.a. Gamification primitives — DigitalSkin (visual rewards),
-- Coin economy, League, Badge, Bounty, Boss challenge, Crafting, Lootbox,
-- Trading cards, Marketplace, Currency converters, Social economy — naturally
-- belong in the Content Consumption domain (learner-side game mechanics).
--
-- NOTE: longer-term, parts of this surface may migrate to chora-sharing's
-- Three-Currency Economy (XP/Coins/Reputation) per ADR. For M12.2 the literal
-- consolidation target per the plan is chora-consumption.
--
-- Source: chora-gamification/migrations/001-018 (up only)
-- Domain: Content Consumption (5 core)
-- Database: chora_consumption
-- RLS: tenant-scoped (preserved verbatim).
-- =============================================================================

BEGIN;

-- ==========================================================================
-- Migration: 001_create_extensions_and_enums.up.sql (gamification consolidation)
-- ==========================================================================
-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-gamification.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Skin rarity levels.
CREATE TYPE skin_rarity AS ENUM (
    'common',
    'uncommon',
    'rare',
    'epic',
    'legendary'
);

-- How a skin was earned.
CREATE TYPE skin_earned_via AS ENUM (
    'streak',
    'league_win',
    'topic_mastery',
    'featured_work',
    'instructor_award',
    'certification',
    'legendary_transfer'
);

-- Display slot for equipped skins.
CREATE TYPE equipment_slot AS ENUM (
    'profile_photo',
    'zoom_overlay',
    'zoom_background',
    'name_badge',
    'email_signature',
    'class_profile'
);

-- External platform for skin export.
CREATE TYPE export_target AS ENUM (
    'zoom',
    'linkedin',
    'email',
    'open_badges'
);

-- Skin export processing status.
CREATE TYPE export_status AS ENUM (
    'pending',
    'completed',
    'failed'
);

-- League competitive tier.
CREATE TYPE league_tier AS ENUM (
    'bronze',
    'silver',
    'gold',
    'platinum',
    'diamond'
);

-- Topic badge mastery level.
CREATE TYPE badge_tier AS ENUM (
    'bronze',
    'silver',
    'gold'
);

-- Knowledge bounty lifecycle status.
-- open → in_progress → completed/expired/cancelled
CREATE TYPE bounty_status AS ENUM (
    'open',
    'in_progress',
    'completed',
    'expired',
    'cancelled'
);

-- Coin transaction direction.
CREATE TYPE coin_transaction_type AS ENUM (
    'earned',
    'spent',
    'refunded'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ==========================================================================
-- Migration: 002_create_digital_skins.up.sql (gamification consolidation)
-- ==========================================================================
-- 002_create_digital_skins.up.sql
-- DigitalSkin: master catalog of all earnable skins.
-- tenant_id is nullable — platform-wide skins have NULL tenant_id.
-- Soft-delete via deleted_at. Aggregate root.

CREATE TABLE digital_skins (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID,                    -- Nullable: NULL = platform-wide skin
    skin_code       VARCHAR(100) NOT NULL UNIQUE,
    name            VARCHAR(255) NOT NULL,
    description     TEXT,
    category        VARCHAR(100) NOT NULL,
    rarity          skin_rarity NOT NULL DEFAULT 'common',
    asset_url       TEXT NOT NULL,
    asset_variants  JSONB DEFAULT '{}',      -- {dark: url, light: url, animated: url}
    earn_criteria   JSONB DEFAULT '{}',      -- Conditions to earn this skin
    is_legendary    BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ              -- Soft delete (DDD rule #5)
);

-- Indexes
CREATE INDEX idx_digital_skins_tenant_id ON digital_skins(tenant_id);
CREATE INDEX idx_digital_skins_rarity ON digital_skins(rarity) WHERE deleted_at IS NULL;
CREATE INDEX idx_digital_skins_category ON digital_skins(category) WHERE deleted_at IS NULL;
CREATE INDEX idx_digital_skins_legendary ON digital_skins(is_legendary) WHERE deleted_at IS NULL AND is_legendary = true;
CREATE UNIQUE INDEX idx_digital_skins_skin_code ON digital_skins(skin_code) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER digital_skins_updated_at
    BEFORE UPDATE ON digital_skins
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
-- Note: tenant_id is nullable for platform skins. Policy allows:
-- 1. Platform skins (tenant_id IS NULL) visible to all tenants
-- 2. Tenant-specific skins visible only to that tenant
ALTER TABLE digital_skins ENABLE ROW LEVEL SECURITY;
ALTER TABLE digital_skins FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON digital_skins
    FOR ALL USING (
        tenant_id IS NULL
        OR tenant_id = current_setting('app.current_tenant_id', true)::uuid
    );

-- ==========================================================================
-- Migration: 003_create_skin_awards.up.sql (gamification consolidation)
-- ==========================================================================
-- 003_create_skin_awards.up.sql
-- SkinAward: record of a skin earned by a learner.
-- Tenant-scoped with RLS. Never deleted (historical record).

CREATE TABLE skin_awards (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    skin_id         UUID NOT NULL REFERENCES digital_skins(id),
    earned_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    earned_via      skin_earned_via NOT NULL,
    earned_context  JSONB DEFAULT '{}'       -- Details: {streak_days: 30, topic: "algebra"}
);

-- Indexes
CREATE INDEX idx_skin_awards_tenant_id ON skin_awards(tenant_id);
CREATE INDEX idx_skin_awards_gcid ON skin_awards(tenant_id, gcid);
CREATE INDEX idx_skin_awards_skin_id ON skin_awards(skin_id);
CREATE UNIQUE INDEX idx_skin_awards_unique_award ON skin_awards(tenant_id, gcid, skin_id);

-- Row-Level Security
ALTER TABLE skin_awards ENABLE ROW LEVEL SECURITY;
ALTER TABLE skin_awards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skin_awards
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 004_create_skin_equipment_exports_legendary.up.sql (gamification consolidation)
-- ==========================================================================
-- 004_create_skin_equipment_exports_legendary.up.sql
-- SkinEquipment, SkinExport, LegendarySkin tables.

-- SkinEquipment: learner's equipped skins per display slot.
-- One skin per slot per learner. Tenant-scoped.
CREATE TABLE skin_equipment (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    slot            equipment_slot NOT NULL,
    skin_award_id   UUID NOT NULL REFERENCES skin_awards(id),
    equipped_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One skin per slot per learner
CREATE UNIQUE INDEX idx_skin_equipment_unique_slot ON skin_equipment(tenant_id, gcid, slot);
CREATE INDEX idx_skin_equipment_gcid ON skin_equipment(tenant_id, gcid);

ALTER TABLE skin_equipment ENABLE ROW LEVEL SECURITY;
ALTER TABLE skin_equipment FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skin_equipment
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- SkinExport: log of skin exports to external platforms.
-- Append-only (never updated after creation).
CREATE TABLE skin_exports (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    skin_award_id   UUID NOT NULL REFERENCES skin_awards(id),
    export_target   export_target NOT NULL,
    exported_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    export_status   export_status NOT NULL DEFAULT 'pending',
    external_ref    VARCHAR(500)             -- External platform reference ID
);

CREATE INDEX idx_skin_exports_tenant_id ON skin_exports(tenant_id);
CREATE INDEX idx_skin_exports_gcid ON skin_exports(tenant_id, gcid);

ALTER TABLE skin_exports ENABLE ROW LEVEL SECURITY;
ALTER TABLE skin_exports FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON skin_exports
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- LegendarySkin: one-holder-at-a-time legendary skin management.
-- Only one active holder per skin (enforced by unique constraint).
CREATE TABLE legendary_skins (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID NOT NULL,
    skin_id               UUID NOT NULL REFERENCES digital_skins(id),
    current_holder_gcid   UUID NOT NULL,     -- Cross-context ref to GCID (no FK)
    held_since            TIMESTAMPTZ NOT NULL DEFAULT now(),
    transfer_reason       VARCHAR(500),
    previous_holders      JSONB DEFAULT '[]', -- [{gcid, held_from, held_until, reason}]
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only one holder per legendary skin per tenant
CREATE UNIQUE INDEX idx_legendary_skins_unique_holder ON legendary_skins(tenant_id, skin_id);
CREATE INDEX idx_legendary_skins_holder ON legendary_skins(current_holder_gcid);

CREATE TRIGGER legendary_skins_updated_at
    BEFORE UPDATE ON legendary_skins
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE legendary_skins ENABLE ROW LEVEL SECURITY;
ALTER TABLE legendary_skins FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON legendary_skins
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 005_create_coin_accounts.up.sql (gamification consolidation)
-- ==========================================================================
-- 005_create_coin_accounts.up.sql
-- CoinAccount: currency balance per learner.
-- CoinTransaction: append-only ledger of all coin movements.

CREATE TABLE coin_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    balance         BIGINT NOT NULL DEFAULT 0,
    lifetime_earned BIGINT NOT NULL DEFAULT 0,
    lifetime_spent  BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_coin_accounts_unique_gcid ON coin_accounts(tenant_id, gcid);
CREATE INDEX idx_coin_accounts_tenant_id ON coin_accounts(tenant_id);

CREATE TRIGGER coin_accounts_updated_at
    BEFORE UPDATE ON coin_accounts
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE coin_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE coin_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coin_accounts
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- CoinTransaction: append-only ledger. Never UPDATE or DELETE.
-- Positive amount = earned, negative amount = spent.
CREATE TABLE coin_transactions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID NOT NULL,
    coin_account_id   UUID NOT NULL REFERENCES coin_accounts(id),
    amount            BIGINT NOT NULL,       -- Positive for earn, negative for spend
    transaction_type  coin_transaction_type NOT NULL,
    reason            VARCHAR(255) NOT NULL,
    reference_id      UUID,                  -- What triggered this (bounty_id, achievement_id, etc.)
    reference_type    VARCHAR(100),          -- "bounty_completion", "daily_dose", "league_reward"
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_coin_transactions_account ON coin_transactions(coin_account_id);
CREATE INDEX idx_coin_transactions_tenant ON coin_transactions(tenant_id);
CREATE INDEX idx_coin_transactions_type ON coin_transactions(tenant_id, transaction_type);
CREATE INDEX idx_coin_transactions_created ON coin_transactions(tenant_id, created_at DESC);

ALTER TABLE coin_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE coin_transactions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON coin_transactions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- Prevent UPDATE and DELETE on append-only coin_transactions.
CREATE POLICY coin_transactions_insert_only ON coin_transactions
    FOR UPDATE USING (false);
CREATE POLICY coin_transactions_no_delete ON coin_transactions
    FOR DELETE USING (false);

-- ==========================================================================
-- Migration: 006_create_leagues.up.sql (gamification consolidation)
-- ==========================================================================
-- 006_create_leagues.up.sql
-- League: weekly competitive brackets per learner.
-- Each row is one learner's standing in one season week.

CREATE TABLE leagues (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    league_tier     league_tier NOT NULL DEFAULT 'bronze',
    season_week     VARCHAR(10) NOT NULL,    -- ISO week: "2026-W12"
    rank            INTEGER NOT NULL DEFAULT 0,
    points          INTEGER NOT NULL DEFAULT 0,
    promoted        BOOLEAN NOT NULL DEFAULT false,
    relegated       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One entry per learner per season week
CREATE UNIQUE INDEX idx_leagues_unique_entry ON leagues(tenant_id, gcid, season_week);
CREATE INDEX idx_leagues_tenant_id ON leagues(tenant_id);
CREATE INDEX idx_leagues_standings ON leagues(tenant_id, season_week, league_tier, points DESC);
CREATE INDEX idx_leagues_gcid ON leagues(tenant_id, gcid);

CREATE TRIGGER leagues_updated_at
    BEFORE UPDATE ON leagues
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

ALTER TABLE leagues ENABLE ROW LEVEL SECURITY;
ALTER TABLE leagues FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON leagues
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 007_create_badges_and_bounties.up.sql (gamification consolidation)
-- ==========================================================================
-- 007_create_badges_and_bounties.up.sql
-- TopicBadge: mastery badges earned per knowledge graph topic.
-- KnowledgeBounty: community challenges for hard atoms.

-- TopicBadge: Bronze/Silver/Gold per topic per learner.
CREATE TABLE topic_badges (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    gcid            UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    topic_id        UUID NOT NULL,           -- Cross-context ref to TopicNode (no FK)
    badge_tier      badge_tier NOT NULL,
    earned_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One badge tier per topic per learner (can upgrade: bronze → silver → gold)
CREATE UNIQUE INDEX idx_topic_badges_unique ON topic_badges(tenant_id, gcid, topic_id, badge_tier);
CREATE INDEX idx_topic_badges_gcid ON topic_badges(tenant_id, gcid);
CREATE INDEX idx_topic_badges_topic ON topic_badges(tenant_id, topic_id);

ALTER TABLE topic_badges ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_badges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON topic_badges
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- KnowledgeBounty: community challenges on hard atoms.
-- Poster spends coins to create, solver earns coins on completion.
CREATE TABLE knowledge_bounties (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               UUID NOT NULL,
    poster_gcid             UUID NOT NULL,   -- Cross-context ref to GCID (no FK)
    atom_id                 UUID NOT NULL,   -- Cross-context ref to LearningAtom (no FK)
    title                   VARCHAR(255) NOT NULL,
    description             TEXT,
    coin_reward             INTEGER NOT NULL CHECK (coin_reward >= 50 AND coin_reward <= 500),
    reputation_requirement  INTEGER NOT NULL DEFAULT 0,
    status                  bounty_status NOT NULL DEFAULT 'open',
    solver_gcid             UUID,            -- Cross-context ref to GCID (no FK)
    solution_text           TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at            TIMESTAMPTZ,
    expires_at              TIMESTAMPTZ NOT NULL,
    deleted_at              TIMESTAMPTZ      -- Soft delete (DDD rule #5)
);

CREATE INDEX idx_bounties_tenant_id ON knowledge_bounties(tenant_id);
CREATE INDEX idx_bounties_status ON knowledge_bounties(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_bounties_poster ON knowledge_bounties(tenant_id, poster_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_bounties_atom ON knowledge_bounties(atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_bounties_expires ON knowledge_bounties(expires_at) WHERE status = 'open' AND deleted_at IS NULL;

ALTER TABLE knowledge_bounties ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_bounties FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_bounties
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 008_processed_events.up.sql (gamification consolidation)
-- ==========================================================================
-- 008_processed_events.up.sql
-- Idempotency table for event processing.
-- Ensures each event is handled exactly once.

CREATE TABLE IF NOT EXISTS processed_events (
    event_id        UUID PRIMARY KEY,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE processed_events ADD COLUMN IF NOT EXISTS event_type VARCHAR(100);

CREATE INDEX idx_processed_events_type ON processed_events(event_type);
CREATE INDEX idx_processed_events_processed ON processed_events(processed_at);

-- ==========================================================================
-- Migration: 009_create_economy_configs.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 009: Economy Configs + History (Choraverse economy parameters)
-- Supports: CHO-357

-- Enum for config categories
CREATE TYPE config_category AS ENUM (
    'fog_thresholds',
    'elo_settings',
    'duel_limits',
    'material_drop_rates',
    'lootbox_probabilities',
    'co_op_hp',
    'boss_difficulty',
    'login_rewards',
    'store_limits'
);

-- Economy configs: tenant-configurable parameters
-- tenant_id IS NULL = platform default
CREATE TABLE economy_configs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID,
    config_category  config_category NOT NULL,
    config_key       VARCHAR(64)     NOT NULL,
    config_value     JSONB           NOT NULL DEFAULT '{}',
    is_tenant_override BOOLEAN       NOT NULL DEFAULT false,
    created_by_gcid  UUID            NOT NULL,
    updated_by_gcid  UUID            NOT NULL,
    created_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ,
    CONSTRAINT uq_economy_config_tenant_category_key
        UNIQUE (tenant_id, config_category, config_key)
);

CREATE INDEX idx_economy_configs_tenant_category
    ON economy_configs (tenant_id, config_category)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_economy_configs_platform_defaults
    ON economy_configs (config_category, config_key)
    WHERE tenant_id IS NULL AND deleted_at IS NULL;

CREATE TRIGGER set_updated_at_economy_configs
    BEFORE UPDATE ON economy_configs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- RLS: tenant isolation (nullable tenant_id — platform defaults visible via explicit query)
ALTER TABLE economy_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE economy_configs FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON economy_configs
    FOR ALL USING (
        tenant_id IS NULL
        OR tenant_id = current_setting('app.current_tenant_id', true)::uuid
    );

-- Economy config history: append-only audit trail
CREATE TABLE economy_config_history (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    economy_config_id UUID        NOT NULL REFERENCES economy_configs(id),
    old_value         JSONB       NOT NULL DEFAULT '{}',
    new_value         JSONB       NOT NULL DEFAULT '{}',
    changed_by_gcid   UUID        NOT NULL,
    change_reason     TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_economy_config_history_config_id
    ON economy_config_history (economy_config_id, created_at DESC);

-- Append-only: prevent UPDATE and DELETE on history
ALTER TABLE economy_config_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE economy_config_history FORCE ROW LEVEL SECURITY;

CREATE POLICY history_read_all ON economy_config_history
    FOR SELECT USING (true);

CREATE POLICY history_insert_only ON economy_config_history
    FOR INSERT WITH CHECK (true);

-- Seed platform defaults (tenant_id IS NULL)
INSERT INTO economy_configs (tenant_id, config_category, config_key, config_value, is_tenant_override, created_by_gcid, updated_by_gcid) VALUES
    (NULL, 'fog_thresholds',      'mastery_threshold',        '{"value": 0.6}',   false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'elo_settings',        'k_factor',                 '{"value": 32}',    false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'duel_limits',         'max_per_day',              '{"value": 10}',    false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'material_drop_rates', 'common_rate',              '{"value": 0.40}',  false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'lootbox_probabilities','real_money_enabled',       '{"value": false}', false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000');

-- ==========================================================================
-- Migration: 010_create_trading_cards.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 010: Trading Card System (TradingCard, CardSet, Instances, Trades)
-- Supports: CHO-326, CHO-327, CHO-328, CHO-329, CHO-330

-- ── Enums ──────────────────────────────────────────────────

CREATE TYPE card_archetype AS ENUM (
    'atom',
    'topic_node',
    'familiar',
    'instructor',
    'legendary'
);

CREATE TYPE card_rarity AS ENUM (
    'common',
    'uncommon',
    'rare',
    'epic',
    'legendary'
);

CREATE TYPE acquired_via AS ENUM (
    'lootbox',
    'boss_reward',
    'co_op_reward',
    'duel_reward',
    'direct_award',
    'trade'
);

CREATE TYPE card_trade_status AS ENUM (
    'in_collection',
    'listed_for_trade',
    'in_transit',
    'traded_away'
);

CREATE TYPE trade_status AS ENUM (
    'proposed',
    'completed',
    'rejected',
    'cancelled'
);

-- ── Card Sets ──────────────────────────────────────────────

CREATE TABLE card_sets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID            NOT NULL,
    set_name            VARCHAR(128)    NOT NULL,
    set_theme           VARCHAR(128),
    release_date        DATE,
    is_limited_edition  BOOLEAN         NOT NULL DEFAULT false,
    total_cards_in_set  INTEGER         NOT NULL CHECK (total_cards_in_set >= 1),
    description         TEXT,
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ,
    UNIQUE (tenant_id, set_name)
);

CREATE INDEX idx_card_sets_tenant ON card_sets (tenant_id) WHERE deleted_at IS NULL;

ALTER TABLE card_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE card_sets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON card_sets
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Trading Cards (templates) ──────────────────────────────

CREATE TABLE trading_cards (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    card_name       VARCHAR(128)    NOT NULL,
    card_archetype  card_archetype  NOT NULL,
    linked_atom_id  UUID,
    linked_topic_id UUID,
    rarity          card_rarity     NOT NULL,
    lore_text       TEXT,
    stat_power      INTEGER         NOT NULL CHECK (stat_power BETWEEN 1 AND 100),
    stat_knowledge  INTEGER         NOT NULL CHECK (stat_knowledge BETWEEN 1 AND 100),
    art_asset_url   TEXT,
    is_tradable     BOOLEAN         NOT NULL DEFAULT true,
    set_id          UUID            REFERENCES card_sets(id),
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_trading_cards_tenant ON trading_cards (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_trading_cards_set ON trading_cards (set_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_trading_cards_rarity ON trading_cards (tenant_id, rarity) WHERE deleted_at IS NULL;

ALTER TABLE trading_cards ENABLE ROW LEVEL SECURITY;
ALTER TABLE trading_cards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON trading_cards
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Trading Card Instances (ownership) ─────────────────────

CREATE TABLE trading_card_instances (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    card_id         UUID            NOT NULL REFERENCES trading_cards(id),
    owner_gcid      UUID            NOT NULL,
    tenant_id       UUID            NOT NULL,
    serial_number   INTEGER         NOT NULL,
    acquired_via    acquired_via    NOT NULL,
    acquired_at     TIMESTAMPTZ     NOT NULL DEFAULT now(),
    is_foil         BOOLEAN         NOT NULL DEFAULT false,
    trade_status    card_trade_status NOT NULL DEFAULT 'in_collection',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    UNIQUE (card_id, serial_number)
);

CREATE INDEX idx_card_instances_owner ON trading_card_instances (tenant_id, owner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_card_instances_card ON trading_card_instances (card_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_card_instances_trade_status ON trading_card_instances (tenant_id, owner_gcid, trade_status) WHERE deleted_at IS NULL;

ALTER TABLE trading_card_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE trading_card_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON trading_card_instances
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Card Trades ────────────────────────────────────────────

CREATE TABLE card_trades (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               UUID            NOT NULL,
    offerer_gcid            UUID            NOT NULL,
    receiver_gcid           UUID            NOT NULL,
    offered_instance_ids    UUID[]          NOT NULL,
    requested_instance_ids  UUID[]          NOT NULL,
    status                  trade_status    NOT NULL DEFAULT 'proposed',
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT now(),
    completed_at            TIMESTAMPTZ,
    deleted_at              TIMESTAMPTZ,
    CONSTRAINT chk_no_self_trade CHECK (offerer_gcid != receiver_gcid)
);

CREATE INDEX idx_card_trades_offerer ON card_trades (tenant_id, offerer_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_card_trades_receiver ON card_trades (tenant_id, receiver_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_card_trades_status ON card_trades (tenant_id, status) WHERE deleted_at IS NULL;

ALTER TABLE card_trades ENABLE ROW LEVEL SECURITY;
ALTER TABLE card_trades FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON card_trades
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Card Trade History (append-only audit) ─────────────────

CREATE TABLE card_trade_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    trade_id        UUID            NOT NULL REFERENCES card_trades(id),
    previous_status trade_status    NOT NULL,
    new_status      trade_status    NOT NULL,
    changed_by_gcid UUID            NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_card_trade_history_trade ON card_trade_history (trade_id);

ALTER TABLE card_trade_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE card_trade_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON card_trade_history
    FOR ALL USING (
        EXISTS (
            SELECT 1 FROM card_trades ct
            WHERE ct.id = card_trade_history.trade_id
            AND ct.tenant_id = current_setting('app.current_tenant_id', true)::uuid
        )
    );

-- Prevent UPDATE/DELETE on audit table (append-only)
CREATE RULE no_update_trade_history AS ON UPDATE TO card_trade_history DO INSTEAD NOTHING;
CREATE RULE no_delete_trade_history AS ON DELETE TO card_trade_history DO INSTEAD NOTHING;

-- ── Card Set Completions (idempotent, one-time) ────────────

CREATE TABLE card_set_completions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    set_id          UUID            NOT NULL REFERENCES card_sets(id),
    completed_at    TIMESTAMPTZ     NOT NULL DEFAULT now(),
    xp_bonus        INTEGER         NOT NULL DEFAULT 0,
    UNIQUE (tenant_id, gcid, set_id)
);

CREATE INDEX idx_card_set_completions_gcid ON card_set_completions (tenant_id, gcid);

ALTER TABLE card_set_completions ENABLE ROW LEVEL SECURITY;
ALTER TABLE card_set_completions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON card_set_completions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 011_economy_balancing.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 011: Economy Balancing & Inflation Control
-- Supports: CHO-749, CHO-750, CHO-751, CHO-752, CHO-753

-- ── Economy Snapshots (daily point-in-time metrics per tenant) ───────────

CREATE TABLE economy_snapshots (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID            NOT NULL,
    snapshot_date      DATE            NOT NULL,
    total_coins        BIGINT          NOT NULL DEFAULT 0,
    daily_minted       BIGINT          NOT NULL DEFAULT 0,
    daily_drained      BIGINT          NOT NULL DEFAULT 0,
    active_accounts    INTEGER         NOT NULL DEFAULT 0,
    inflation_rate_pct DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    created_at         TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT uq_economy_snapshot_tenant_date
        UNIQUE (tenant_id, snapshot_date)
);

CREATE INDEX idx_economy_snapshots_tenant_date
    ON economy_snapshots (tenant_id, snapshot_date DESC);

-- RLS: tenant isolation
ALTER TABLE economy_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE economy_snapshots FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON economy_snapshots
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Seed new EconomyConfig platform defaults (tenant_id IS NULL) ─────────

INSERT INTO economy_configs (tenant_id, config_category, config_key, config_value, is_tenant_override, created_by_gcid, updated_by_gcid) VALUES
    (NULL, 'store_limits',         'territory_maintenance_cost', '{"daily_cost_per_territory": 5}',               false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'material_drop_rates',  'material_shelf_life',        '{"shelf_life_days": 30}',                       false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'elo_settings',         'elo_inactivity_decay',       '{"inactive_days_threshold": 14, "decay_amount": 25}', false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000'),
    (NULL, 'store_limits',         'inflation_target',           '{"max_daily_mint_pct": 5}',                     false, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000');

-- ==========================================================================
-- Migration: 012_boss_challenges.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 012: Boss Challenges
-- Supports: CHO-338, CHO-339

-- ── Boss Challenges ─────────────────────────────────────────────────────

CREATE TABLE boss_challenges (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    boss_name       VARCHAR(128)    NOT NULL,
    topic_node_id   UUID            NOT NULL,
    difficulty_tier TEXT            NOT NULL CHECK (difficulty_tier IN ('normal', 'hard', 'legendary')),
    required_score  INTEGER         NOT NULL,
    reward_xp       INTEGER         NOT NULL,
    reward_coins    INTEGER         NOT NULL,
    reward_card_id  UUID,
    status          TEXT            NOT NULL CHECK (status IN ('scheduled', 'active', 'completed', 'cancelled')),
    scheduled_at    TIMESTAMPTZ     NOT NULL DEFAULT now(),
    starts_at       TIMESTAMPTZ     NOT NULL,
    ends_at         TIMESTAMPTZ     NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_boss_challenges_tenant_status
    ON boss_challenges (tenant_id, status) WHERE deleted_at IS NULL;

CREATE INDEX idx_boss_challenges_tenant_starts
    ON boss_challenges (tenant_id, starts_at DESC) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE boss_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE boss_challenges FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON boss_challenges
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Boss Challenge Participants ─────────────────────────────────────────

CREATE TABLE boss_challenge_participants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    challenge_id    UUID            NOT NULL REFERENCES boss_challenges(id),
    gcid            UUID            NOT NULL,
    score           INTEGER         NOT NULL DEFAULT 0,
    completed_at    TIMESTAMPTZ,
    reward_claimed  BOOLEAN         NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT uq_boss_participant UNIQUE (challenge_id, gcid)
);

CREATE INDEX idx_boss_participants_challenge
    ON boss_challenge_participants (challenge_id);

CREATE INDEX idx_boss_participants_gcid
    ON boss_challenge_participants (tenant_id, gcid);

-- RLS: tenant isolation
ALTER TABLE boss_challenge_participants ENABLE ROW LEVEL SECURITY;
ALTER TABLE boss_challenge_participants FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON boss_challenge_participants
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 013_crafting_system.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 013: Crafting System
-- Supports: CHO-344, CHO-345

-- ── Materials ───────────────────────────────────────────────────────────

CREATE TABLE materials (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    material_name   VARCHAR(128)    NOT NULL,
    material_type   TEXT            NOT NULL CHECK (material_type IN ('common', 'uncommon', 'rare', 'epic')),
    description     TEXT            NOT NULL DEFAULT '',
    icon_url        TEXT            NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_materials_tenant
    ON materials (tenant_id) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE materials ENABLE ROW LEVEL SECURITY;
ALTER TABLE materials FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON materials
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Material Inventory ──────────────────────────────────────────────────

CREATE TABLE material_inventory (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    material_id     UUID            NOT NULL REFERENCES materials(id),
    quantity        INTEGER         NOT NULL CHECK (quantity >= 0),
    acquired_at     TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT uq_material_inventory UNIQUE (tenant_id, gcid, material_id)
);

CREATE INDEX idx_material_inventory_gcid
    ON material_inventory (tenant_id, gcid) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE material_inventory ENABLE ROW LEVEL SECURITY;
ALTER TABLE material_inventory FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON material_inventory
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Crafting Recipes ────────────────────────────────────────────────────

CREATE TABLE crafting_recipes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID            NOT NULL,
    recipe_name         VARCHAR(128)    NOT NULL,
    description         TEXT            NOT NULL DEFAULT '',
    input_materials     JSONB           NOT NULL DEFAULT '[]'::jsonb,
    output_type         TEXT            NOT NULL CHECK (output_type IN ('material', 'card', 'skin', 'coins', 'xp')),
    output_id           UUID,
    output_quantity     INTEGER         NOT NULL DEFAULT 1,
    craft_time_seconds  INTEGER         NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX idx_crafting_recipes_tenant
    ON crafting_recipes (tenant_id) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE crafting_recipes ENABLE ROW LEVEL SECURITY;
ALTER TABLE crafting_recipes FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON crafting_recipes
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 014_lootbox_daily_rewards.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 014: Lootbox & Daily Rewards
-- Supports: CHO-347, CHO-348

-- ── Lootboxes ───────────────────────────────────────────────────────────

CREATE TABLE lootboxes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    lootbox_tier    TEXT            NOT NULL CHECK (lootbox_tier IN ('standard', 'premium', 'legendary')),
    source          TEXT            NOT NULL CHECK (source IN ('level_up', 'boss_reward', 'daily_login', 'purchase', 'event')),
    is_opened       BOOLEAN         NOT NULL DEFAULT false,
    opened_at       TIMESTAMPTZ,
    reward_summary  JSONB,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_lootboxes_gcid
    ON lootboxes (tenant_id, gcid) WHERE deleted_at IS NULL;

CREATE INDEX idx_lootboxes_unopened
    ON lootboxes (tenant_id, gcid) WHERE is_opened = false AND deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE lootboxes ENABLE ROW LEVEL SECURITY;
ALTER TABLE lootboxes FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON lootboxes
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Loot Tables ─────────────────────────────────────────────────────────

CREATE TABLE loot_tables (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    tier            TEXT            NOT NULL,
    item_type       TEXT            NOT NULL CHECK (item_type IN ('card', 'material', 'coins', 'xp', 'skin')),
    item_id         UUID,
    quantity_min    INTEGER         NOT NULL,
    quantity_max    INTEGER         NOT NULL,
    weight          INTEGER         NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_loot_tables_tier
    ON loot_tables (tenant_id, tier);

-- RLS: tenant isolation
ALTER TABLE loot_tables ENABLE ROW LEVEL SECURITY;
ALTER TABLE loot_tables FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON loot_tables
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Login Calendar ──────────────────────────────────────────────────────

CREATE TABLE login_calendar (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    login_date      DATE            NOT NULL,
    streak_day      INTEGER         NOT NULL,
    reward_claimed  BOOLEAN         NOT NULL DEFAULT false,
    reward_type     TEXT            NOT NULL DEFAULT '',
    reward_value    INTEGER         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT uq_login_calendar UNIQUE (tenant_id, gcid, login_date)
);

CREATE INDEX idx_login_calendar_gcid
    ON login_calendar (tenant_id, gcid, login_date DESC);

-- RLS: tenant isolation
ALTER TABLE login_calendar ENABLE ROW LEVEL SECURITY;
ALTER TABLE login_calendar FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON login_calendar
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 015_territory_rewards.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 015: Territory Rewards (Deep Rewards Economy)
-- Supports: CHO-528, CHO-529, CHO-530, CHO-531, CHO-532, CHO-534, CHO-536

-- ── Territory Income Config ───────────────────────────────────────────

CREATE TABLE territory_income_configs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               UUID            NOT NULL,
    income_type             TEXT            NOT NULL CHECK (income_type IN ('xp', 'coins', 'stars')),
    base_amount             INTEGER         NOT NULL,
    multiplier_per_territory FLOAT           NOT NULL DEFAULT 1.0,
    frequency               TEXT            NOT NULL CHECK (frequency IN ('daily', 'weekly')),
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at              TIMESTAMPTZ
);

CREATE INDEX idx_territory_income_configs_tenant
    ON territory_income_configs (tenant_id, income_type) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE territory_income_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE territory_income_configs FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON territory_income_configs
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Territory Income Logs ─────────────────────────────────────────────

CREATE TABLE territory_income_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    income_type     TEXT            NOT NULL CHECK (income_type IN ('xp', 'coins', 'stars')),
    amount          INTEGER         NOT NULL,
    territory_count INTEGER         NOT NULL,
    period_start    DATE            NOT NULL,
    period_end      DATE            NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_territory_income_logs_gcid
    ON territory_income_logs (tenant_id, gcid, period_start DESC);

CREATE INDEX idx_territory_income_logs_period
    ON territory_income_logs (tenant_id, period_start, period_end);

-- RLS: tenant isolation
ALTER TABLE territory_income_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE territory_income_logs FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON territory_income_logs
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Seasonal Leaderboard Rewards ──────────────────────────────────────

CREATE TABLE seasonal_leaderboard_rewards (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    season_id       VARCHAR(64)     NOT NULL,
    rank_tier       TEXT            NOT NULL CHECK (rank_tier IN ('top_1', 'top_3', 'top_10', 'top_25', 'top_50')),
    reward_type     TEXT            NOT NULL,
    reward_amount   INTEGER         NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_seasonal_leaderboard_rewards_season
    ON seasonal_leaderboard_rewards (tenant_id, season_id);

-- RLS: tenant isolation
ALTER TABLE seasonal_leaderboard_rewards ENABLE ROW LEVEL SECURITY;
ALTER TABLE seasonal_leaderboard_rewards FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON seasonal_leaderboard_rewards
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 016_marketplace.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 016: Marketplace
-- Supports: CHO-754, CHO-755, CHO-756, CHO-757, CHO-758

-- ── Market Listings ───────────────────────────────────────────────────

CREATE TABLE market_listings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    seller_gcid     UUID            NOT NULL,
    item_type       TEXT            NOT NULL CHECK (item_type IN ('card', 'material', 'lootbox', 'skin')),
    item_id         UUID            NOT NULL,
    quantity        INTEGER         NOT NULL,
    price_coins     INTEGER         NOT NULL,
    price_stars     INTEGER,
    status          TEXT            NOT NULL CHECK (status IN ('active', 'sold', 'expired', 'cancelled')),
    listed_at       TIMESTAMPTZ     NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ     NOT NULL,
    sold_at         TIMESTAMPTZ,
    buyer_gcid      UUID,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_market_listings_active
    ON market_listings (tenant_id, status, item_type) WHERE status = 'active' AND deleted_at IS NULL;

CREATE INDEX idx_market_listings_seller
    ON market_listings (tenant_id, seller_gcid) WHERE deleted_at IS NULL;

-- RLS: tenant isolation
ALTER TABLE market_listings ENABLE ROW LEVEL SECURITY;
ALTER TABLE market_listings FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON market_listings
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Market Orders ─────────────────────────────────────────────────────

CREATE TABLE market_orders (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    listing_id      UUID            NOT NULL REFERENCES market_listings(id),
    buyer_gcid      UUID            NOT NULL,
    payment_method  TEXT            NOT NULL CHECK (payment_method IN ('coins', 'stars')),
    amount_paid     INTEGER         NOT NULL,
    status          TEXT            NOT NULL CHECK (status IN ('pending', 'completed', 'failed', 'refunded')),
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_market_orders_listing
    ON market_orders (tenant_id, listing_id);

CREATE INDEX idx_market_orders_buyer
    ON market_orders (tenant_id, buyer_gcid);

-- RLS: tenant isolation
ALTER TABLE market_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE market_orders FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON market_orders
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Market Transactions (APPEND ONLY) ─────────────────────────────────

CREATE TABLE market_transactions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    order_id        UUID            NOT NULL REFERENCES market_orders(id),
    seller_gcid     UUID            NOT NULL,
    buyer_gcid      UUID            NOT NULL,
    item_type       TEXT            NOT NULL,
    item_id         UUID            NOT NULL,
    price           INTEGER         NOT NULL,
    fee_amount      INTEGER         NOT NULL DEFAULT 0,
    net_amount      INTEGER         NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_market_transactions_order
    ON market_transactions (tenant_id, order_id);

CREATE INDEX idx_market_transactions_seller
    ON market_transactions (tenant_id, seller_gcid);

CREATE INDEX idx_market_transactions_buyer
    ON market_transactions (tenant_id, buyer_gcid);

-- APPEND ONLY: prevent UPDATE and DELETE on market_transactions
CREATE RULE market_transactions_no_update AS ON UPDATE TO market_transactions DO INSTEAD NOTHING;
CREATE RULE market_transactions_no_delete AS ON DELETE TO market_transactions DO INSTEAD NOTHING;

-- RLS: tenant isolation
ALTER TABLE market_transactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE market_transactions FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON market_transactions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 017_currency_converters.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 017: Currency Converters
-- Supports: CHO-761

CREATE TABLE currency_conversions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    from_currency   TEXT            NOT NULL CHECK (from_currency IN ('coins', 'stars')),
    to_currency     TEXT            NOT NULL CHECK (to_currency IN ('coins', 'stars')),
    from_amount     INTEGER         NOT NULL,
    to_amount       INTEGER         NOT NULL,
    exchange_rate   FLOAT           NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_currency_conversions_gcid
    ON currency_conversions (tenant_id, gcid, created_at DESC);

-- RLS: tenant isolation
ALTER TABLE currency_conversions ENABLE ROW LEVEL SECURITY;
ALTER TABLE currency_conversions FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON currency_conversions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 018_social_economy.up.sql (gamification consolidation)
-- ==========================================================================
-- Migration 018: Social Economy
-- Supports: CHO-762, CHO-763, CHO-764

-- ── Currency Transfers (P2P Gifting) ──────────────────────────────────

CREATE TABLE currency_transfers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    sender_gcid     UUID            NOT NULL,
    receiver_gcid   UUID            NOT NULL,
    currency        TEXT            NOT NULL CHECK (currency IN ('coins', 'stars')),
    amount          INTEGER         NOT NULL,
    message         VARCHAR(256),
    status          TEXT            NOT NULL CHECK (status IN ('pending', 'completed', 'rejected')),
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    CHECK (sender_gcid != receiver_gcid)
);

CREATE INDEX idx_currency_transfers_sender
    ON currency_transfers (tenant_id, sender_gcid, created_at DESC);

CREATE INDEX idx_currency_transfers_receiver
    ON currency_transfers (tenant_id, receiver_gcid);

-- RLS: tenant isolation
ALTER TABLE currency_transfers ENABLE ROW LEVEL SECURITY;
ALTER TABLE currency_transfers FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON currency_transfers
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Group Fund Pools ──────────────────────────────────────────────────

CREATE TABLE group_fund_pools (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    pool_name       VARCHAR(128)    NOT NULL,
    pool_type       TEXT            NOT NULL CHECK (pool_type IN ('boss_challenge', 'co_op_reward', 'seasonal')),
    target_amount   INTEGER         NOT NULL,
    current_amount  INTEGER         NOT NULL DEFAULT 0,
    status          TEXT            NOT NULL CHECK (status IN ('collecting', 'distributed', 'cancelled')),
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_group_fund_pools_status
    ON group_fund_pools (tenant_id, status);

-- RLS: tenant isolation
ALTER TABLE group_fund_pools ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_fund_pools FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON group_fund_pools
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── Group Fund Contributions ──────────────────────────────────────────

CREATE TABLE group_fund_contributions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    pool_id         UUID            NOT NULL REFERENCES group_fund_pools(id),
    gcid            UUID            NOT NULL,
    amount          INTEGER         NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_group_fund_contributions_pool
    ON group_fund_contributions (tenant_id, pool_id);

CREATE INDEX idx_group_fund_contributions_gcid
    ON group_fund_contributions (tenant_id, gcid);

-- RLS: tenant isolation
ALTER TABLE group_fund_contributions ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_fund_contributions FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON group_fund_contributions
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);


COMMIT;

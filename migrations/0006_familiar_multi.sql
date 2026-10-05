-- =============================================================================
-- chora-consumption : 0006_familiar_multi.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : M12.2.B Familiar consolidation (2026-05-12)
-- Architecture  : ADR-116 amendment (1:N Familiar per user) + ADR-147 §7 +
--                 docs/architecture/multi-familiar-per-user-2026-05-11.md +
--                 .claude/skills/domain-content-consumption/SKILL.md §Multi-Familiar
--
-- Subsumes: chora-familiar/migrations/002_create_familiars.up.sql (Python service
-- migrations — the chora-familiar Go scaffolding is archived in Batch 5 per
-- docs/architecture/m12-2-consolidation-plan-2026-05-12.md).
--
-- Multi-Familiar (1:N) aggregate tables:
--
--   * familiar_instances        — 1:N companion entity per learner
--   * familiar_skill_catalog    — platform-managed skill registry
--   * familiar_skill_grants     — bridge (which skills assigned to which Instance)
--   * familiar_progression_tiers — reference data (apprentice/adept/master/sage)
--
-- The legacy 1:1 `familiars` table from 0001_initial.sql is RETAINED for
-- backwards compatibility with the existing /api/familiars + /familiar/me
-- endpoints (Phyllis MVP). A separate M14.1 migration will deprecate it
-- once the BFF layer redirects all callers to /v1/me/familiars.
--
-- Per ddd-enforcement.md + multi-tenant-rls SKILL: every tenant-scoped
-- table gets RLS via `chora.tenant_id` session GUC (defence-in-depth
-- complementing the per-database domain isolation).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- familiar_instances — multi-Familiar (1:N) per learner
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_instances (
    familiar_id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID         NOT NULL,
    owner_gcid               UUID         NOT NULL,
    name                     TEXT         NOT NULL,
    specialization           TEXT         NOT NULL,            -- 'math' | 'history' | 'coding' | 'music' | ...
    evolution_tier           TEXT         NOT NULL DEFAULT 'apprentice'
        CHECK (evolution_tier IN ('apprentice', 'adept', 'master', 'sage')),
    skill_slots_unlocked     INT          NOT NULL DEFAULT 1 CHECK (skill_slots_unlocked >= 0),
    memory_context_capacity  INT          NOT NULL DEFAULT 1000 CHECK (memory_context_capacity >= 0),
    persona_summary          TEXT,
    configured_rules         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at               TIMESTAMPTZ
);

CREATE INDEX idx_familiar_instances_owner_gcid
    ON familiar_instances (tenant_id, owner_gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_familiar_instances_specialization
    ON familiar_instances (tenant_id, specialization)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_familiar_instances_updated_at
    BEFORE UPDATE ON familiar_instances
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE familiar_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_instances
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- familiar_skill_catalog — platform-managed skill registry
--
-- Each entry maps a skill_key (e.g., 'math.solver') to an ADK Go tool
-- handler name resolved at session-time per ADR-147 §7. The
-- xp_unlock_threshold gates user assignment (skill becomes available to
-- the learner once their cumulative XP crosses the threshold).
--
-- Platform-scoped (no tenant_id) — every tenant sees the same catalogue.
-- Skill changes are platform-managed releases, not tenant data.
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_skill_catalog (
    skill_id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_key             TEXT         NOT NULL UNIQUE,
    name                  TEXT         NOT NULL,
    description           TEXT,
    tool_handler_ref      TEXT         NOT NULL,                -- ADK Go tool registration name
    xp_unlock_threshold   INT          NOT NULL DEFAULT 0 CHECK (xp_unlock_threshold >= 0),
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ
);

CREATE INDEX idx_familiar_skill_catalog_active
    ON familiar_skill_catalog (skill_key)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_familiar_skill_catalog_updated_at
    BEFORE UPDATE ON familiar_skill_catalog
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

-- No RLS — platform-scoped table; every tenant reads.

-- -----------------------------------------------------------------------------
-- familiar_skill_grants — bridge between Instances and the catalogue
--
-- Per docs/architecture/multi-familiar-per-user-2026-05-11.md §2.1: a
-- grant means "the learner has equipped this skill on this Familiar". The
-- skill_slots_unlocked cap is enforced at the domain entity level + the
-- service layer (see internal/domain/familiar/instance.go GrantSkill).
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_skill_grants (
    grant_id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID         NOT NULL,
    familiar_id      UUID         NOT NULL REFERENCES familiar_instances(familiar_id) ON DELETE RESTRICT,
    skill_id         UUID         NOT NULL REFERENCES familiar_skill_catalog(skill_id) ON DELETE RESTRICT,
    granted_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ,
    UNIQUE (familiar_id, skill_id)
);

CREATE INDEX idx_familiar_skill_grants_familiar
    ON familiar_skill_grants (familiar_id)
    WHERE deleted_at IS NULL;

ALTER TABLE familiar_skill_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_skill_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_skill_grants
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- familiar_progression_tiers — reference data for the XP-gated tier curve
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_progression_tiers (
    tier_name                TEXT         PRIMARY KEY
        CHECK (tier_name IN ('apprentice', 'adept', 'master', 'sage')),
    xp_required              INT          NOT NULL CHECK (xp_required >= 0),
    skill_slots              INT          NOT NULL CHECK (skill_slots >= 0),
    memory_context_capacity  INT          NOT NULL CHECK (memory_context_capacity >= 0),
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT now()
);

INSERT INTO familiar_progression_tiers (tier_name, xp_required, skill_slots, memory_context_capacity) VALUES
    ('apprentice',      0,  1,  1000),
    ('adept',        5000,  3,  5000),
    ('master',      25000,  5, 15000),
    ('sage',       100000,  8, 50000);

-- No RLS — reference data; platform-scoped.

COMMIT;

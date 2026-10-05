-- =============================================================================
-- chora-consumption : 0041_familiar_voice_and_accent.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-115 (Familiar voice synthesis architecture) +
--                 ADR-116 (Familiar age maturation + voice accent system)
-- Architecture  : docs/architecture/adrs/adr-115-familiar-voice-synthesis.md
--                 docs/architecture/adrs/adr-116-familiar-age-voice-accent.md
--                 docs/references/ddd-aggregate-map.md §3.2 + §10 (audit A1)
-- Audit reference: ~/.claude/plans/transient-hugging-dewdrop.md §4.1 A1 + §16.2 I.1
--
-- Purpose:
--   Author the two tables that ADR-115 / ADR-116 commit but earlier audits flagged
--   as missing. The voice config table holds the per-Familiar voice persona +
--   accent + speed/pitch settings; the accent_unlock table is an append-only
--   audit of unlock events as the Familiar's growth stage advances.
--
-- Aggregate boundary:
--   `Familiar` aggregate root in chora_consumption (see ddd-aggregate-map §3.2).
--   `familiar_voice_config` is a child entity (one row per familiar_id, soft-delete
--   on disable). `familiar_accent_unlock` is a separate APPEND-ONLY audit aggregate
--   pattern (no UPDATE/DELETE — new rows record each unlock event).
--
-- Soft-delete on voice_config per .claude/rules/ddd-enforcement.md §5.
-- Append-only on accent_unlock per .claude/rules/ddd-enforcement.md §4 (parallel to
-- AtomRevision, GCIDMerge, InvigilatorIncidentReport, A2AInvocation, TenantManaAllocation).
-- RLS-enabled — composes with multi-tenant-rls skill.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- familiar_voice_config — per-Familiar voice persona configuration
--
-- One row per familiar_id (enforced by UNIQUE partial index WHERE deleted_at IS NULL).
-- voice_id identifies the upstream provider voice (e.g. 'familiar-voice-bright-1').
-- accent_id identifies the per-stage accent overlay; nullable for the neutral
-- default. provider_preference is an enum-style hint (`google_tts`, `elevenlabs`,
-- `azure_cognitive`, `chora_self_hosted`) routed by the voice synthesis sidecar
-- per ADR-115 §"Sidecar routing".
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_voice_config (
    voice_config_id        UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              UUID         NOT NULL,
    familiar_id            UUID         NOT NULL REFERENCES familiar_instances(familiar_id),

    voice_id               TEXT         NOT NULL CHECK (length(voice_id) > 0),
    accent_id              TEXT,
    pitch_semitones        SMALLINT     NOT NULL DEFAULT 0
        CHECK (pitch_semitones BETWEEN -12 AND 12),
    speed_factor           NUMERIC(3,2) NOT NULL DEFAULT 1.00
        CHECK (speed_factor BETWEEN 0.50 AND 2.00),

    provider_preference    TEXT         NOT NULL DEFAULT 'google_tts'
        CHECK (provider_preference IN (
            'google_tts',
            'elevenlabs',
            'azure_cognitive',
            'chora_self_hosted'
        )),
    enabled                BOOLEAN      NOT NULL DEFAULT TRUE,

    created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at             TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_familiar_voice_config_one_per_familiar
    ON familiar_voice_config (familiar_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_familiar_voice_config_tenant
    ON familiar_voice_config (tenant_id, familiar_id)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_familiar_voice_config_updated_at
    BEFORE UPDATE ON familiar_voice_config
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE familiar_voice_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_voice_config FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_voice_config
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- familiar_accent_unlock — append-only audit of accent unlock events
--
-- A Familiar's accent set evolves as growth_stage advances per ADR-116. Each
-- unlock event records (familiar_id, accent_id, unlocked_at_growth_stage,
-- unlock_method, unlocked_by_gcid). Rows are immutable post-write — see the
-- trigger below that rejects UPDATE and DELETE.
-- -----------------------------------------------------------------------------
CREATE TABLE familiar_accent_unlock (
    unlock_id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id              UUID         NOT NULL,
    familiar_id            UUID         NOT NULL REFERENCES familiar_instances(familiar_id),

    accent_id              TEXT         NOT NULL CHECK (length(accent_id) > 0),
    unlocked_at_growth_stage SMALLINT   NOT NULL
        CHECK (unlocked_at_growth_stage BETWEEN 0 AND 6),
    unlock_method          TEXT         NOT NULL
        CHECK (unlock_method IN (
            'age_maturation',
            'item_use',
            'tenant_grant',
            'admin_manual'
        )),
    unlocked_by_gcid       UUID,
    evidence               JSONB        NOT NULL DEFAULT '{}'::jsonb,

    unlocked_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- A given accent unlocks once per Familiar — partial unique index.
CREATE UNIQUE INDEX idx_familiar_accent_unlock_one_per_familiar_accent
    ON familiar_accent_unlock (familiar_id, accent_id);

CREATE INDEX idx_familiar_accent_unlock_tenant_familiar
    ON familiar_accent_unlock (tenant_id, familiar_id, unlocked_at);

-- Append-only enforcement at the SQL layer — the same pattern used by
-- atom_revisions, gcid_merge, invigilator_incident_report,
-- tenant_mana_allocation, familiar_growth_events.
CREATE OR REPLACE FUNCTION reject_familiar_accent_unlock_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'familiar_accent_unlock is append-only (ADR-116 §accent unlock semantics)';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_familiar_accent_unlock_no_update
    BEFORE UPDATE ON familiar_accent_unlock
    FOR EACH ROW EXECUTE FUNCTION reject_familiar_accent_unlock_mutation();

CREATE TRIGGER trg_familiar_accent_unlock_no_delete
    BEFORE DELETE ON familiar_accent_unlock
    FOR EACH ROW EXECUTE FUNCTION reject_familiar_accent_unlock_mutation();

ALTER TABLE familiar_accent_unlock ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_accent_unlock FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiar_accent_unlock
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- Grants are handled by services/chora-consumption/migrations/9999_grant_app_roles.sql
-- which re-runs lex-last on every migration job + sets ALTER DEFAULT
-- PRIVILEGES for the migrate role. New tables created here automatically
-- inherit app_rw / app_ro grants via that default-privileges mechanism.

COMMIT;

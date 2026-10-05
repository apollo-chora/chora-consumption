-- =============================================================================
-- chora-consumption : 0112_companion_suspension_projection.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-254 D11 (advisory projection) of ADR-252 (companion
--                 containment control), 2026-08-23
-- Architecture  : docs/architecture/adrs/adr-252-companion-containment-control.md
--                 docs/architecture/adrs/adr-254-full-events-orchestration-and-companion-rename.md
--                 chora-contracts/proto/events/governance/audit.proto
--                   (CompanionSuspensionChanged)
--
-- Purpose:
--   LOCAL, ADVISORY read-copy of the operator's companion containment state,
--   projected from chora.governance.audit.companion_suspension_changed.v1.
--   chora-observability OWNS the two suspension tables (platform_companion_
--   suspension + companion_suspension_policy, its migration 0018) and the
--   operator write path; chora-model-gateway READS them uncached on every
--   companion turn and is the CONTROL (deny-before-debit, fail-closed).
--   chora-consumption cannot read chora_observability (cross-DB queries are
--   FORBIDDEN per .claude/rules/ddd-enforcement.md) and the operator GET route
--   is PLATFORM_OPERATOR-only, so this table is the learner-safe mirror used
--   to refuse a chat turn with 403 COMPANION_SUSPENDED before the mana
--   pre-check / ADR-235 claim and to render `companion_status.paused`.
--   A stale row here is a UX wobble, never a containment breach.
--
-- Shape:
--   One row per observability suspension id (the aggregate identity), holding
--   the LATEST state seen. source_version is the emitter's per-row monotonic
--   counter (engage = 1, release = 2); the adapter applies an event only when
--   it is strictly newer (version, then changed_at), exactly like the
--   external_egress_policy projection in chora_observability. A release is a
--   state change on the row (engaged = FALSE), never a DELETE: the lift
--   history survives and the read filters engaged = TRUE.
--
-- RLS (why this is NOT the plain tenant_isolation template):
--   The table holds PLATFORM-scope rows (tenant_id IS NULL, "every tenant") as
--   well as tenant-scope rows. The two obvious shapes were rejected:
--     * RLS disabled (like companion_progression_tiers / companion_skill_
--       catalog, which are tenant-less reference data): this table DOES carry
--       tenant rows, and one tenant's admin-imposed containment reason must
--       not be readable by another tenant.
--     * tenant_isolation (tenant_id = GUC): a platform row has no tenant and
--       would be invisible to every tenant, which is the exact row the learner
--       most needs to see.
--   So: ENABLE + FORCE ROW LEVEL SECURITY with ONE policy that admits
--   `tenant_id IS NULL` rows to everyone plus the caller's own tenant rows.
--   The cast is NULLIF-safe (an unset or empty GUC compares NULL and therefore
--   contributes nothing, instead of raising 22P02). Writes by the projector run
--   under SET LOCAL chora.tenant_id = <event tenant> for tenant rows and under
--   the nil UUID sentinel for platform rows (the policy admits tenant_id IS
--   NULL for any GUC; the value only has to be UUID-shaped). Reads run under
--   the learner's tenant. This widens NOTHING: a tenant still sees only its
--   own rows plus the platform rows that apply to it by construction, so the
--   ADR-165/184/192 RLS-bypass budget is untouched (not a 4th surface).
--
-- Grants: services/chora-consumption/migrations/9999_grant_app_roles.sql
-- (re-run lex-last on every migration job; new tables inherit the app_rw /
-- app_ro grants through ALTER DEFAULT PRIVILEGES), no explicit GRANT here.
-- =============================================================================

BEGIN;

CREATE TABLE companion_suspension_projection (
    -- The observability suspension row id (UUIDv7 minted by the emitter);
    -- the aggregate identity this projection is keyed on.
    suspension_id    UUID         PRIMARY KEY,

    -- 'platform' = every tenant; 'tenant' = the one tenant named below.
    scope            TEXT         NOT NULL CHECK (scope IN ('platform', 'tenant')),

    -- NULL for platform scope (see the CHECK below and the RLS policy).
    tenant_id        UUID,

    -- The mana action code the containment names; NULL = every companion turn
    -- in the scope (ADR-254 D4's action_codes[] is this single value).
    skill_key        TEXT         CHECK (skill_key IS NULL OR length(skill_key) > 0),

    -- TRUE = engaged (contained), FALSE = released (lifted). Released rows are
    -- kept; the advisory read filters engaged = TRUE.
    engaged          BOOLEAN      NOT NULL,

    -- The operator's mandatory reason for the LATEST change (engage reason
    -- while engaged, release reason once released).
    reason           TEXT         NOT NULL CHECK (length(reason) > 0),
    actor_gcid       UUID         NOT NULL,

    -- Emitter's per-row monotonic version; the apply guard. engage = 1,
    -- release = 2, so >= 1 always.
    source_version   BIGINT       NOT NULL CHECK (source_version >= 1),

    -- Server time of the change on the emitter side; secondary ordering key.
    changed_at       TIMESTAMPTZ  NOT NULL,

    -- The event that produced the held state (traceability into the
    -- governance audit trail).
    last_event_id    UUID         NOT NULL,

    projected_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT companion_suspension_projection_scope_tenant_chk CHECK (
        (scope = 'platform' AND tenant_id IS NULL)
        OR (scope = 'tenant' AND tenant_id IS NOT NULL)
    )
);

-- The advisory read: every ENGAGED row the tenant may see (platform + own).
-- Partial on engaged so the index stays the size of the live containment set.
CREATE INDEX idx_companion_suspension_projection_engaged
    ON companion_suspension_projection (scope, tenant_id)
    WHERE engaged = TRUE;

-- -----------------------------------------------------------------------------
-- Row-Level Security: platform rows for everyone, tenant rows for their own
-- tenant only (rationale in the header). rls.ApplySession sets
-- SET LOCAL chora.tenant_id before every statement.
-- -----------------------------------------------------------------------------
ALTER TABLE companion_suspension_projection ENABLE ROW LEVEL SECURITY;
ALTER TABLE companion_suspension_projection FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_or_own_tenant ON companion_suspension_projection
    FOR ALL
    USING (
        tenant_id IS NULL
        OR tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
    )
    WITH CHECK (
        tenant_id IS NULL
        OR tenant_id = NULLIF(current_setting('chora.tenant_id', true), '')::uuid
    );

COMMIT;

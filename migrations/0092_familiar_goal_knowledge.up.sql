-- chora-consumption : 0092_familiar_goal_knowledge.up.sql
-- CHO-2118 (tier-2 cached goal-memory synthesis) — the Companion tab's per-goal
-- reflection: a short first-person narrative of what the learner's Companion
-- remembers about them on ONE goal ("feel known by my companion, not logged by
-- it").
--
-- Domain   : Content Consumption (Team 1) — chora_consumption
-- Design   : docs/design/cho-2118-goal-memory-synthesis-design.md (owner-signed
--            2026-07-14: mana EXEMPT, 6-event invalidation set, 6h floor /
--            14d max-age, own read-model table, key by goal_id).
--
-- What lands here:
--
--  * familiar_goal_knowledge — a DERIVED read-model cache, one live row per
--    (tenant, learner, familiar, goal). It is NOT a source of truth: the sources
--    are familiar_memory_recall (ADR-173, append-only), the goal + its concept
--    subtree, and learner_weakness. This table caches the LLM synthesis over
--    them so the tab read is sub-millisecond and LLM-free on the hot path.
--
--    Lifecycle mirrors kg_hexagon_nodes (ADR-143): materialise → cache-hit-on-
--    read → event-invalidate → lazy-regen → decision-stamp. Two columns carry
--    the load:
--
--      - status: an invalidated row goes 'stale' but KEEPS synthesis_text, so
--        the learner still sees the last good reflection (marked "reflecting…")
--        instead of a blank — serve-stale-while-regen. Only a never-generated
--        row is 'pending' with no text.
--      - requested_at: the in-flight claim. N concurrent tab reads must not each
--        fire an LLM call; the first stamps requested_at and the rest stand down
--        until it lands or the crash-window TTL lapses.
--
--    root_concept_id is the ADR-214 re-root guard: a goal that has re-rooted is
--    about a DIFFERENT subtree, so a reflection generated against the old root
--    is invalid regardless of freshness.
--
--    generated_by_run_id / generated_by_model_id / prompt_version are the
--    ADR-197 decision stamp — a cached LLM artefact that cannot say which prompt
--    and model produced it is not explainable, so they are mandatory on write
--    (enforced in the Go domain; NULL only while never-generated).
--
-- Mana: the synthesis is EXEMPT at the metering seam (an un-catalogued
-- action_code, exactly as campaign_free_reveal is per ADR-227 D2) — it is a
-- passive display, not a learner-initiated action, and it is cached so regens
-- are rare. Cost is bounded by the TTL floor, NOT by mana. Cloud Model Armor and
-- the chora-model-gateway chokepoint (ADR-163/177) stay FULLY in force — this is
-- an exemption from metering, never a bypass of the guardrail.
--
-- Idioms: 0080 RLS (ENABLE + FORCE + tenant_isolation on chora.tenant_id — the
-- CURRENT GUC, never the legacy app.current_tenant_id that CHO-2140 is sweeping);
-- UUIDv7 minted in the Go domain layer (gen_random_uuid() is only the DB-side
-- fallback); soft-delete; live-row partial unique index; grants come from
-- 9999_grant_app_roles.sql via ALTER DEFAULT PRIVILEGES (no inline grant).
-- Additive only; no backfill, so no NO-FORCE toggle txn is needed.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'familiar_goal_knowledge_status') THEN
        CREATE TYPE familiar_goal_knowledge_status AS ENUM ('pending', 'fresh', 'stale');
    END IF;
END$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'familiar_goal_knowledge_invalidation_reason') THEN
        CREATE TYPE familiar_goal_knowledge_invalidation_reason AS ENUM (
            'never_generated',        -- pseudo-state: a fresh row, never invalidated
            'goal_rerooted',          -- ADR-214 re-root: the reflection is about another subtree now
            'goal_graduated',
            'weakness_grown',
            'weakness_analyzed',
            'goal_progress_updated',
            'memory_evicted',
            'chat_turn_completed',    -- highest churn, weakest signal — the TTL floor exists for this one
            'max_age_expired',
            'manual_admin'
        );
    END IF;
END$$;

CREATE TABLE IF NOT EXISTS familiar_goal_knowledge (
    id                    UUID  PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             UUID  NOT NULL,
    learner_gcid          UUID  NOT NULL,
    familiar_id           UUID  NOT NULL,   -- the goal's bound Companion; cross-aggregate ref, no FK
    goal_id               UUID  NOT NULL,   -- the reflection IS about the goal; cross-aggregate ref, no FK

    synthesis_text        TEXT,             -- last good reflection; NULL only before the first synthesis, NEVER blanked by an invalidation
    status                familiar_goal_knowledge_status NOT NULL DEFAULT 'pending',

    -- ADR-197 decision stamp. NULL only while never-generated.
    generated_by_run_id   UUID,
    generated_by_model_id TEXT,
    prompt_version        TEXT,
    content_hash          TEXT,             -- hash of the synthesis INPUTS — lets a regen detect input drift
    generated_at          TIMESTAMPTZ,

    root_concept_id       UUID,             -- ADR-214 re-root guard; NULL for a goal with no root

    requested_at          TIMESTAMPTZ,      -- in-flight synthesis claim; NULL when nothing is pending
    invalidated_at        TIMESTAMPTZ,
    invalidation_reason   familiar_goal_knowledge_invalidation_reason NOT NULL DEFAULT 'never_generated',

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at            TIMESTAMPTZ       -- soft-delete (#4) — never hard-delete
);

-- One live reflection per (tenant, learner, familiar, goal). This is the upsert
-- target on the lazy-regen write path; soft-deleted rows fall out of it.
CREATE UNIQUE INDEX IF NOT EXISTS uq_familiar_goal_knowledge_live
    ON familiar_goal_knowledge (tenant_id, learner_gcid, familiar_id, goal_id)
    WHERE deleted_at IS NULL;

-- The invalidation subscribers fan out by (learner, goal) — one weakness.grown
-- can stale several goals' reflections — so index the live lookup they drive.
CREATE INDEX IF NOT EXISTS ix_familiar_goal_knowledge_goal_live
    ON familiar_goal_knowledge (tenant_id, learner_gcid, goal_id)
    WHERE deleted_at IS NULL;

-- ...and by (learner, familiar) for the memory-scoped events (chat turn,
-- eviction), which stale every goal that Companion reflects on.
CREATE INDEX IF NOT EXISTS ix_familiar_goal_knowledge_familiar_live
    ON familiar_goal_knowledge (tenant_id, learner_gcid, familiar_id)
    WHERE deleted_at IS NULL;

ALTER TABLE familiar_goal_knowledge ENABLE ROW LEVEL SECURITY;
ALTER TABLE familiar_goal_knowledge FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON familiar_goal_knowledge;
CREATE POLICY tenant_isolation ON familiar_goal_knowledge
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE familiar_goal_knowledge IS
    'CHO-2118 tier-2 cached goal-memory synthesis: one live row per (tenant, learner_gcid, familiar_id, goal_id) holding the Companion''s per-goal reflection. A DERIVED read-model cache over familiar_memory_recall + goal subtree + learner_weakness — never a source of truth. Invalidated by event, lazily regenerated, and served stale-while-regen (an invalidated row keeps synthesis_text). Mana-EXEMPT at the metering seam (un-catalogued action_code, per ADR-227 D2 precedent); Model Armor + the model-gateway chokepoint remain in force. Cost is bounded by the TTL floor, not by mana.';

COMMENT ON COLUMN familiar_goal_knowledge.requested_at IS
    'In-flight synthesis claim: the first tab read stamps it, concurrent reads stand down. Re-issued once DefaultPendingTTL lapses (crash window) so a lost request cannot strand the learner on "reflecting..." forever.';

COMMENT ON COLUMN familiar_goal_knowledge.root_concept_id IS
    'ADR-214 re-root guard: the goal root the reflection was generated against. A re-rooted goal describes a different subtree, so a row whose root no longer matches is invalid regardless of freshness.';

COMMIT;

-- chora-consumption : 0077_familiar_campaign.up.sql
-- WS-C0 of ADR-227 (the Familiar Campaign — fog-of-war conquest of the
-- learner-sovereign Knowledge Graph) — CHO-2079. Composes with ADR-228
-- (Incubation Arc) via the chora.consumption.campaign.*.v1 events.
--
-- Domain   : Content Consumption (Team 1) — chora_consumption
-- Spec     : ADR-227 D16 (DDD placement) + its Verification addendum
--            (BINDING on the build); grilling record 2026-07-09.
--
-- What lands here:
--
--  * campaign_node_progress — per-(tenant, learner, concept) ladder state.
--    The 6 rungs use the ORIGINAL-Bloom vocabulary in ADR-227 D6 ladder
--    order (knowledge -> comprehension -> application -> analysis ->
--    evaluation -> synthesis; rung 6 = synthesis, the revised-Bloom
--    "create" summit). rungs_cleared counts strictly-ordered cleared
--    rungs; rung_cleared_at[i] stamps when rung i cleared (positional
--    audit for D10 verified-XP); won_at is the D9 permanent ratchet (only
--    legal at 6/6); last_advance_date backs the D7 one-rung-per-calendar-
--    day pacing gate. Deliberately NO goal_id column: campaign state is
--    per-(learner, node) and survives ADR-214 re-roots untouched (D5) —
--    events derive goal_id at emit time.
--
--  * concept_node_lineage — D14 merge/split ancestry on BOTH identity
--    axes (node UUID + concept_key slug) so WS-C5 can refuse XP re-awards
--    across lineage and WS-C6 can repair the slug-keyed joins
--    (goals.concept_set, learner_weakness.concept_key,
--    topic_retention.topic_id rows, proofing keys — addendum #7).
--
--  * goals.focus_concept_id — D11: the single campaign focus per goal
--    (nullable; cross-aggregate UUID, no FK per ddd-enforcement #3).
--    WS-C1 reconciles it with familiar_instances.resonant_concept_id
--    (addendum #5).
--
-- Idioms: 0056 RLS (ENABLE + FORCE + tenant_isolation on chora.tenant_id;
-- per-learner scoping is an explicit query predicate in Go); UUIDv7 minted
-- in the Go domain layer (gen_random_uuid() is only the DB-side fallback);
-- soft-delete; live-row partial indexes; grants come from
-- 9999_grant_app_roles.sql via ALTER DEFAULT PRIVILEGES (no inline grant).
-- Additive only; no backfill, so no NO-FORCE toggle txn is needed.

BEGIN;

CREATE TABLE IF NOT EXISTS campaign_node_progress (
    id                 UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID          NOT NULL,
    learner_gcid       UUID          NOT NULL,
    concept_id         UUID          NOT NULL,   -- concept_nodes.id; same-DB cross-aggregate ref, no FK (merge/split soft-deletes nodes)
    rungs_cleared      SMALLINT      NOT NULL DEFAULT 0,
    rung_cleared_at    TIMESTAMPTZ[] NOT NULL DEFAULT '{}',  -- position i = rung i cleared-at (strictly ordered ladder)
    won_at             TIMESTAMPTZ,                          -- D9 ratchet: set once at 6/6, never unset by time
    last_advance_date  DATE,                                 -- D7: at most one rung advance per calendar day
    created_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,                          -- soft-delete (#5)
    CONSTRAINT campaign_progress_rungs_range        CHECK (rungs_cleared BETWEEN 0 AND 6),
    CONSTRAINT campaign_progress_rung_audit_aligned CHECK (cardinality(rung_cleared_at) = rungs_cleared),
    CONSTRAINT campaign_progress_won_is_full_ladder CHECK (won_at IS NULL OR rungs_cleared = 6)
);

-- One live ladder row per (tenant, learner, concept); soft-deleted rows
-- (merge/split retirements) fall out of the constraint.
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_node_progress_live
    ON campaign_node_progress (tenant_id, learner_gcid, concept_id)
    WHERE deleted_at IS NULL;

ALTER TABLE campaign_node_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign_node_progress FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON campaign_node_progress;
CREATE POLICY tenant_isolation ON campaign_node_progress
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE campaign_node_progress IS
    'ADR-227 campaign ladder state per (tenant, learner_gcid, concept_id): rungs_cleared 0-6 in D6 ladder order, rung_cleared_at positional audit, won_at D9 ratchet, last_advance_date D7 pacing gate. No goal_id by design (D5: survives re-root).';
COMMENT ON COLUMN campaign_node_progress.rung_cleared_at IS
    'Positional audit: element i is the cleared-at of rung i (1-based ladder knowledge..synthesis); cardinality always equals rungs_cleared';

CREATE TABLE IF NOT EXISTS concept_node_lineage (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID         NOT NULL,
    learner_gcid      UUID         NOT NULL,
    operation         VARCHAR(16)  NOT NULL CHECK (operation IN ('merge', 'split')),
    from_concept_id   UUID         NOT NULL,   -- merge: absorbed node; split: parent
    to_concept_id     UUID         NOT NULL,   -- merge: survivor;     split: child
    from_concept_key  TEXT         NOT NULL,   -- slug axis (addendum #7): minted-once-from-title, NOT identity
    to_concept_key    TEXT         NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ,             -- soft-delete (#5)
    CONSTRAINT concept_node_lineage_no_self CHECK (from_concept_id <> to_concept_id)
);

-- Lineage is walked in both directions: "did XP already flow to an
-- ancestor?" (C5) and "which children inherited?" (C6).
CREATE INDEX IF NOT EXISTS idx_concept_node_lineage_from
    ON concept_node_lineage (tenant_id, learner_gcid, from_concept_id)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_concept_node_lineage_to
    ON concept_node_lineage (tenant_id, learner_gcid, to_concept_id)
    WHERE deleted_at IS NULL;

ALTER TABLE concept_node_lineage ENABLE ROW LEVEL SECURITY;
ALTER TABLE concept_node_lineage FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON concept_node_lineage;
CREATE POLICY tenant_isolation ON concept_node_lineage
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE concept_node_lineage IS
    'ADR-227 D14 merge/split ancestry, dual-axis (concept UUID + concept_key slug). C5 blocks XP re-award across lineage; C6 join-repair walks it. Append-only in practice; soft-delete only.';

ALTER TABLE goals
    ADD COLUMN IF NOT EXISTS focus_concept_id UUID;  -- ADR-227 D11 campaign focus; no FK (#3)

COMMENT ON COLUMN goals.focus_concept_id IS
    'ADR-227 D11: the single campaign focus node of this goal (concept_nodes.id, no FK). WS-C1 reconciles with familiar_instances.resonant_concept_id (addendum #5).';

COMMIT;

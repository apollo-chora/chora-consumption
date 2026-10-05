-- chora-consumption : 0080_campaign_reveal_ledger.up.sql
-- WS-C4 of ADR-227 (the Familiar Campaign — fog-of-war conquest of the
-- learner-sovereign Knowledge Graph) — CHO-2083. Implements D2 fog gating:
-- winning a campaign node reveals its neighbours for free (the conquest
-- reward), driven off chora.consumption.campaign.node_won.v1.
--
-- Domain   : Content Consumption (Team 1) — chora_consumption
-- Spec     : ADR-227 D2 (fog gating) + its Verification addendum (BINDING on
--            the build); grilling record 2026-07-09.
--
-- What lands here:
--
--  * campaign_reveal_ledger — the redelivery-safe record that makes "exactly
--    one free reveal per node win" hold under Pub/Sub at-least-once delivery.
--    The reveal consumer runs a claim-then-mark protocol: Claim INSERTs on the
--    live semantic identity (tenant, learner, goal, concept) with ON CONFLICT
--    DO NOTHING, publishes the fog-reveal suggestion request, then MarkPublished
--    stamps request_id + published_at. On redelivery the claim collides: a
--    stamped published_at means a true duplicate (ack); a NULL means the publish
--    crashed mid-flight (re-publish then re-mark).
--
--    The semantic identity is the primary dedupe bound (won_at is a permanent
--    ratchet, so a node is won — and node_won fires — exactly once). The
--    node_won_event_id UNIQUE is the belt to those braces: it hard-stops a
--    double-fire (two distinct event ids for the same win) from ever minting a
--    second reveal, per the ADR-227 addendum.
--
-- Idioms: 0056 RLS (ENABLE + FORCE + tenant_isolation on chora.tenant_id;
-- per-learner scoping is an explicit query predicate in Go); UUIDv7 minted in
-- the Go domain layer (gen_random_uuid() is only the DB-side fallback);
-- soft-delete; live-row partial indexes; grants come from
-- 9999_grant_app_roles.sql via ALTER DEFAULT PRIVILEGES (no inline grant).
-- Additive only; no backfill, so no NO-FORCE toggle txn is needed.

BEGIN;

CREATE TABLE IF NOT EXISTS campaign_reveal_ledger (
    id                 UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          UUID          NOT NULL,
    learner_gcid       UUID          NOT NULL,
    goal_id            UUID          NOT NULL,   -- reveal is scoped to the goal the win was emitted under; cross-aggregate ref, no FK
    concept_id         UUID          NOT NULL,   -- the won node whose neighbours are revealed; concept_nodes.id, no FK
    node_won_event_id  UUID          NOT NULL,   -- the node_won.v1 event_id — the dedupe belt (addendum)
    request_id         UUID,                     -- the published fog-reveal suggestion request; set by MarkPublished (step 2)
    published_at       TIMESTAMPTZ,              -- when the suggestion request published; NULL until MarkPublished
    created_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ               -- soft-delete (#5)
);

-- One live reveal per (tenant, learner, goal, concept): the semantic
-- <=1-reveal-per-node-win bound the product cares about. Claim arbitrates the
-- ON CONFLICT here; soft-deleted rows fall out of the constraint.
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_reveal_ledger_live
    ON campaign_reveal_ledger (tenant_id, learner_gcid, goal_id, concept_id)
    WHERE deleted_at IS NULL;

-- The event-id dedupe belt: a double-fire (distinct event ids for the same
-- win) can never mint a second live reveal (ADR-227 addendum).
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_reveal_ledger_event
    ON campaign_reveal_ledger (node_won_event_id)
    WHERE deleted_at IS NULL;

ALTER TABLE campaign_reveal_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign_reveal_ledger FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON campaign_reveal_ledger;
CREATE POLICY tenant_isolation ON campaign_reveal_ledger
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE campaign_reveal_ledger IS
    'ADR-227 D2 free-on-win fog-reveal ledger (WS-C4, CHO-2083): one live row per (tenant, learner_gcid, goal_id, concept_id) makes exactly-one-reveal-per-node-win hold under Pub/Sub redelivery via claim-then-mark. node_won_event_id UNIQUE is the double-fire belt; request_id + published_at record the publish step.';

COMMIT;

-- chora-consumption : 0108_atom_refresh_ledger.up.sql
-- ADR-244 D5 standing refresh trigger (the surviving half after the ADR's own
-- correction killed the one-time sweep): when chora.creation.atom.published.v1
-- or atom.updated.v1 lands, the Familiar may OFFER the atom to reachable
-- concepts as a pending ConceptSuggestion. Proposals only, zero atom_refs
-- writes (ADR-244 D1; a backfill migration that stuffs atom_refs is forbidden
-- by D1 and would be a D9 violation wearing a migration's costume).
--
-- Domain   : Content Consumption (Team 1), chora_consumption
-- Spec     : ADR-244 D5 + ADR-212 D9 (refresh suggestions, not authority)
--
-- What lands here:
--
--  * atom_refresh_ledger, the redelivery-safe AND backfill-safe record that
--    bounds the trigger to ONE proposal request per (tenant, learner, atom,
--    focal concept), ever. The subscriber runs the 0080 claim-then-mark
--    protocol: Claim INSERTs on the live semantic identity with ON CONFLICT
--    DO NOTHING, publishes the suggestion request, then MarkPublished stamps
--    request_id + published_at. A claimed-but-unpublished row is either the
--    crash-window (re-publish on redelivery) or the audit trace of a
--    post-claim skip (atom not entitled for the learner, per-day cap).
--
--    Event-id dedupe alone would NOT hold here: creation's backfill-published
--    and backfill-topic-tags re-emit atom.published for every atom of a
--    tenant under fresh event ids, so the semantic identity is the bound.
--    trigger_event_id is audit only, deliberately NOT unique (one atom event
--    fans out to several pairings).
--
-- Idioms: 0056 RLS (ENABLE + FORCE + tenant_isolation on chora.tenant_id;
-- per-learner scoping is an explicit query predicate in Go); UUIDv7 minted in
-- the Go domain layer (gen_random_uuid() is only the DB-side fallback);
-- soft-delete; live-row partial indexes; grants come from
-- 9999_grant_app_roles.sql via ALTER DEFAULT PRIVILEGES (no inline grant).
-- Additive only; no backfill, so no NO-FORCE toggle txn is needed.

BEGIN;

CREATE TABLE IF NOT EXISTS atom_refresh_ledger (
    id                UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID          NOT NULL,
    learner_gcid      UUID          NOT NULL,
    atom_id           UUID          NOT NULL,   -- the published/updated LearningAtom; cross-domain ref, no FK
    focal_concept_id  UUID          NOT NULL,   -- concept_nodes.id the proposal targets; no FK
    trigger_event_id  UUID          NOT NULL,   -- the atom event that minted this claim; audit, NOT unique
    request_id        UUID,                     -- the published suggestion request; set by MarkPublished
    published_at      TIMESTAMPTZ,              -- when the suggestion request published; NULL until MarkPublished
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ               -- soft-delete
);

-- One live proposal per (tenant, learner, atom, focal): the once-ever bound
-- that makes the trigger redelivery-safe and backfill-storm-safe. Claim
-- arbitrates the ON CONFLICT here; soft-deleted rows fall out.
CREATE UNIQUE INDEX IF NOT EXISTS uq_atom_refresh_ledger_live
    ON atom_refresh_ledger (tenant_id, learner_gcid, atom_id, focal_concept_id)
    WHERE deleted_at IS NULL;

-- The per-day cap read (CountClaimedSince): live claims for a learner by
-- creation time.
CREATE INDEX IF NOT EXISTS idx_atom_refresh_ledger_learner_day
    ON atom_refresh_ledger (tenant_id, learner_gcid, created_at)
    WHERE deleted_at IS NULL;

ALTER TABLE atom_refresh_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE atom_refresh_ledger FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON atom_refresh_ledger;
CREATE POLICY tenant_isolation ON atom_refresh_ledger
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE atom_refresh_ledger IS
    'ADR-244 D5 standing refresh trigger ledger: one live row per (tenant, learner_gcid, atom_id, focal_concept_id) bounds the atom.published/atom.updated proposal fan-out to a single suggestion request per pairing under Pub/Sub redelivery and creation-side backfill re-emits, via the 0080 claim-then-mark protocol. trigger_event_id is audit only; request_id + published_at record the publish step.';

COMMIT;

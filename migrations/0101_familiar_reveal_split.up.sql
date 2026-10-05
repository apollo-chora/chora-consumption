-- =============================================================================
-- chora-consumption : 0101_familiar_reveal_split.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Story    : CHO-2229 (ADR-228 Phase 2 amendment — reveal before naming)
--
-- Why: the breed roll is split OUT of the HatchEgg commit into its own
-- POST /v1/me/familiars/{id}/reveal step so the learner names a companion
-- they can SEE (owner decision 2026-07-16 #2). `revealed_at` is the moment
-- the roll happened and was persisted:
--
--   * NULL      — unrevealed pod (species is NULL too, the CHO-2227 mystery
--                 invariant) or a born-hatched row that never walks the
--                 ceremony... except see the backfill below.
--   * NOT NULL  — the roll is locked. HatchEgg (commit-only since CHO-2229)
--                 requires it and reads the persisted roll; a re-POST of
--                 /reveal returns this roll verbatim and never re-rolls.
--
-- Backfill: every already-hatched row (ceremony-hatched AND born-hatched)
-- gets revealed_at = hatched_at — under the old contract the roll happened
-- inside the hatch commit, so that IS its reveal moment. This keeps the
-- invariant total: species non-empty ⇒ revealed_at non-null.
--
-- Unopened pods (stage 0, hatched_at IS NULL) stay revealed_at NULL and must
-- walk the new reveal step before they can commit.
-- =============================================================================

BEGIN;

ALTER TABLE familiar_instances
  ADD COLUMN IF NOT EXISTS revealed_at timestamptz NULL;

COMMENT ON COLUMN familiar_instances.revealed_at IS
  'CHO-2229: the breed-roll moment (POST /reveal). NULL until the pod is revealed; species is NULL exactly while this is. Hatch is commit-only and requires it. Backfilled = hatched_at for pre-split rows (the roll used to happen inside the hatch commit).';

-- Cross-tenant owner-run one-time backfill: FORCE RLS is enabled on this
-- table, so toggle it off for the UPDATE and restore it immediately — atomic
-- within this transaction (mirrors 0046:53-60 + 0100).
ALTER TABLE familiar_instances NO FORCE ROW LEVEL SECURITY;

UPDATE familiar_instances
   SET revealed_at = hatched_at
 WHERE hatched_at IS NOT NULL
   AND revealed_at IS NULL;

ALTER TABLE familiar_instances FORCE ROW LEVEL SECURITY;

COMMIT;

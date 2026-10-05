-- =============================================================================
-- chora-consumption : 0054_atom_index_playable_status.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-1968, 2026-06-30
-- Architecture  : internal/domain/atom_index/atom_index.go (AtomIndex.Status +
--                 Playable()/IsAnswerable()); projected from
--                 chora.creation.atom.published.v1 (creationv1.AtomStatus enum).
--
-- Purpose:
--   The daily dose was serving DRAFT / question-less atoms because atom_index
--   carried no lifecycle state and nothing flipped it on publish. This adds a
--   `status` column (draft | published | archived) so the dose universe can
--   filter to PUBLISHED + answerable atoms only.
--
--   The DEFAULT 'draft' backfills every existing row to NOT-playable: an atom
--   only becomes playable once an atom.published event runs AtomIndexRepo
--   .MarkPublished (which flips status -> published and sets the answer key).
--   atom.created seeds new rows as 'draft'; the upsert's status merge is
--   non-downgrading so a late created replay never reverts a published row.
--
--   atom_index is tenant-scoped only; it ENABLEs RLS with the tenant_isolation
--   policy FOR ALL (migration 0004). The new column inherits that policy — no
--   new policy required.
-- =============================================================================

BEGIN;

ALTER TABLE atom_index
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'draft';

COMMIT;

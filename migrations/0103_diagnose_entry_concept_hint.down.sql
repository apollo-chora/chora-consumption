-- =============================================================================
-- chora-consumption : 0103_diagnose_entry_concept_hint.down.sql (reverse of .up)
-- ADR-238 D2 - drop the soft entry-concept hint column. Data loss on the single
-- nullable column is acceptable: it is additive, has no FK, no index, and no
-- downstream projection. Dropping it reverts ranking to unbiased (pre-D2), which
-- is exactly the behaviour the tie-band kill-switch already produces.
-- =============================================================================

BEGIN;

ALTER TABLE weakness_doc_uploads
    DROP COLUMN IF EXISTS entry_concept_id;

COMMIT;

-- =============================================================================
-- chora-consumption : 0055_weakness_review_pending.down.sql  (reverse of .up.sql)
-- =============================================================================
BEGIN;

ALTER TABLE weakness_doc_uploads DROP COLUMN IF EXISTS review_payload;

-- Restore the pre-0055 status set (rejects 'AWAITING_REVIEW'). Re-added NOT VALID
-- so the rollback is ATOMIC and never fails on any AWAITING_REVIEW rows the
-- feature may already have parked (we never hard-delete to fit a narrowed
-- constraint); new/updated rows are still checked, so post-rollback transitions
-- to AWAITING_REVIEW are rejected as before 0055.
ALTER TABLE weakness_doc_uploads
    DROP CONSTRAINT IF EXISTS weakness_doc_uploads_status_check;
ALTER TABLE weakness_doc_uploads
    ADD CONSTRAINT weakness_doc_uploads_status_check
    CHECK (status IN ('QUEUED', 'ANALYZING', 'COMPLETED', 'FAILED')) NOT VALID;

COMMIT;

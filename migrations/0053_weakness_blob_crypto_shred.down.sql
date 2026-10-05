-- =============================================================================
-- chora-consumption : 0053_weakness_blob_crypto_shred.down.sql  (reverse of .up.sql)
-- =============================================================================
BEGIN;

ALTER TABLE weakness_doc_uploads DROP COLUMN IF EXISTS blob_deleted_at;
ALTER TABLE weakness_doc_uploads DROP COLUMN IF EXISTS upload_rights_consent_at;

-- Restore the pre-0053 kind set (rejects NEW 'source_material'). Re-added
-- NOT VALID so the rollback is ATOMIC and never fails on any source_material
-- rows that the feature may already have written (we never hard-delete to fit a
-- narrowed constraint); new/updated rows are still checked, so post-rollback
-- source_material inserts are rejected as before 0053.
ALTER TABLE weakness_doc_uploads
    DROP CONSTRAINT IF EXISTS weakness_doc_uploads_upload_kind_check;
ALTER TABLE weakness_doc_uploads
    ADD CONSTRAINT weakness_doc_uploads_upload_kind_check
    CHECK (upload_kind IN ('marked_test', 'notes', 'scribble')) NOT VALID;

DROP TABLE IF EXISTS weakness_blob_dek_wrap;

COMMIT;

-- =============================================================================
-- chora-consumption : 0053_weakness_blob_crypto_shred.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-205 WS-5 — Growth-Edge analyser privacy/crypto-shred
--                 (CHO-1957), 2026-06-29
-- Architecture  : docs/architecture/adrs/adr-205-growth-edge-analyser-graduation.md (D8)
--                 docs/architecture/adrs/adr-186-crypto-shred-envelope-encryption.md
--                 internal/domain/weakness_blob/* (envelope + shredder + sweeper)
--
-- Purpose:
--   The Growth-Edge raw upload (marked papers / notes / scribbles / a grounding
--   textbook) is the highest-PII/IP input in the learning loop. ADR-205 D8 +
--   ADR-186 mandate: persist only the distilled Descriptor, never the raw blob.
--   To make "transient" a real crypto guarantee (GDPR Art.17 / PDPA "destroyed")
--   rather than a best-effort GCS delete, each blob is envelope-encrypted with a
--   RANDOM per-blob DEK (AES-256-GCM); the DEK is wrapped by a Cloud KMS master
--   KEK and only the WRAPPED form is persisted here. Crypto-shred = tombstone the
--   wrapped-DEK row (scrub the bytes) -> every ciphertext copy (incl. GCS backups)
--   becomes permanently undecryptable. Per-blob (deletable on a single
--   diagnosis-complete event), distinct from the per-USER account-closure DEK.
--
--   Also extends weakness_doc_uploads for WS-5:
--     - upload_kind gains 'source_material' (a grounding textbook — TRANSIENT;
--       distilled to a scope-profile, never persisted verbatim; gated by a
--       one-tap upload-rights consent recorded in upload_rights_consent_at);
--     - blob_deleted_at records the delete-on-analyse of the GCS ciphertext.
--
-- Soft-delete only (deleted_at) per .claude/rules/ddd-enforcement.md §4 — the
-- wrapped-DEK row is tombstoned + scrubbed, NEVER hard-deleted. RLS-enabled
-- (multi-tenant-rls); per-learner scoping is an explicit learner_gcid predicate
-- in every query. NB: the wrapped-DEK row holds NO plaintext key material — only
-- the master-KEK-wrapped ciphertext, which is itself useless once tombstoned.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- weakness_blob_dek_wrap — one row per Growth-Edge upload's wrapped per-blob DEK.
-- 1:1 (logical) with weakness_doc_uploads.upload_id; kept as its own crypto
-- lifecycle (no FK — the upload row is soft-deleted, the DEK row is tombstoned;
-- they shred on different triggers). Deleting the wrapped_dek bytes IS the
-- crypto-shred.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS weakness_blob_dek_wrap (
    upload_id     UUID         PRIMARY KEY,             -- logical 1:1 with weakness_doc_uploads
    tenant_id     UUID         NOT NULL,
    learner_gcid  UUID         NOT NULL,
    wrapped_dek   BYTEA,                                -- master-KEK-wrapped per-blob DEK; NULL after crypto-shred
    kek_version   TEXT         NOT NULL,                -- KMS key-version name (re-wrap on rotation)
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ                           -- crypto-shred tombstone (wrapped_dek scrubbed to NULL)
);

-- Sweep hot path: the TTL backstop lists live (un-shredded) rows by age.
CREATE INDEX IF NOT EXISTS idx_weakness_blob_dek_wrap_sweep
    ON weakness_blob_dek_wrap (created_at) WHERE deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation. Every
-- read/write runs SET LOCAL chora.tenant_id (rls.ApplySession) first; the
-- per-tenant TTL sweep relies on this (it scopes by the ctx tenant — no
-- cross-tenant scan, no RLS-bypass surface).
-- -----------------------------------------------------------------------------
ALTER TABLE weakness_blob_dek_wrap ENABLE ROW LEVEL SECURITY;
ALTER TABLE weakness_blob_dek_wrap FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON weakness_blob_dek_wrap;
CREATE POLICY tenant_isolation ON weakness_blob_dek_wrap
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- weakness_doc_uploads — WS-5 additions: source_material kind + consent + the
-- delete-on-analyse marker. Additive + backward-compatible (existing rows keep
-- NULL for the two new columns; the CHECK only widens the allowed kind set).
-- -----------------------------------------------------------------------------
ALTER TABLE weakness_doc_uploads
    DROP CONSTRAINT IF EXISTS weakness_doc_uploads_upload_kind_check;
ALTER TABLE weakness_doc_uploads
    ADD CONSTRAINT weakness_doc_uploads_upload_kind_check
    CHECK (upload_kind IN ('marked_test', 'notes', 'scribble', 'source_material'));

ALTER TABLE weakness_doc_uploads
    ADD COLUMN IF NOT EXISTS upload_rights_consent_at TIMESTAMPTZ;  -- one-tap rights consent (source_material gate)
ALTER TABLE weakness_doc_uploads
    ADD COLUMN IF NOT EXISTS blob_deleted_at TIMESTAMPTZ;           -- GCS ciphertext delete-on-analyse marker

-- Grants handled by 9999_grant_app_roles.sql (ALTER DEFAULT PRIVILEGES; re-runs
-- lex-last on every migration job — no explicit GRANT here).

COMMIT;

-- =============================================================================
-- chora-consumption : 0047_weakness_doc_uploads.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : Epic-1 1b — Growth-Edge upload-job lifecycle (W8b), 2026-06-10
-- Architecture  : ~/.claude/plans/warm-drifting-crab.md (W8)
--                 chora-contracts/openapi/consumption-growth-edges.yaml (FROZEN —
--                   GrowthEdgeUploadJob: upload_id + status + upserted ids + reason)
--                 internal/domain/weakness_upload/* (job aggregate + ports)
--
-- Purpose:
--   The async lifecycle of one uploaded weakness document. POST .../uploads
--   stores the blob, inserts a QUEUED row here, and publishes
--   chora.consumption.weakness_doc.uploaded.v1; the ai-kernel analyser crew
--   processes it and chora.consumption.weakness.analyzed.v1 flips this row to
--   COMPLETED (+ the upserted Growth-Edge ids). GET .../uploads/{id} polls it.
--
--   The raw blob is transient (cold-archived / discarded post-analysis) — this
--   row is descriptive job state, never the document.
--
-- Soft-delete only (`deleted_at`) per .claude/rules/ddd-enforcement.md §5.
-- RLS-enabled (multi-tenant-rls); per-learner scoping is an explicit
-- learner_gcid predicate in every query. UUIDv7 upload_id minted in the domain.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- weakness_doc_uploads — one row per uploaded weakness document (the job).
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS weakness_doc_uploads (
    upload_id         UUID         PRIMARY KEY,
    tenant_id         UUID         NOT NULL,
    learner_gcid      UUID         NOT NULL,
    upload_kind       VARCHAR(16)  NOT NULL
                                   CHECK (upload_kind IN ('marked_test', 'notes', 'scribble')),
    source_mime       TEXT         NOT NULL,
    source_blob_uri   TEXT         NOT NULL,                  -- gs://... transient blob
    status            VARCHAR(16)  NOT NULL DEFAULT 'QUEUED'
                                   CHECK (status IN ('QUEUED', 'ANALYZING', 'COMPLETED', 'FAILED')),
    upserted_edge_ids UUID[]       NOT NULL DEFAULT '{}',     -- present on COMPLETED
    edge_count        INT          NOT NULL DEFAULT 0,
    failure_reason    TEXT,                                   -- present on FAILED (non-leaky)
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    analyzed_at       TIMESTAMPTZ,                            -- set on COMPLETED / FAILED
    deleted_at        TIMESTAMPTZ                             -- soft-delete per ddd-enforcement §5
);

-- Hot path: poll is by (learner, upload_id); the learner's recent-uploads list
-- is per-learner over live rows.
CREATE INDEX IF NOT EXISTS idx_weakness_doc_uploads_learner
    ON weakness_doc_uploads (tenant_id, learner_gcid) WHERE deleted_at IS NULL;

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- Every read/write runs SET LOCAL chora.tenant_id (rls.ApplySession) first.
-- -----------------------------------------------------------------------------
ALTER TABLE weakness_doc_uploads ENABLE ROW LEVEL SECURITY;
ALTER TABLE weakness_doc_uploads FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON weakness_doc_uploads;
CREATE POLICY tenant_isolation ON weakness_doc_uploads
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants handled by 9999_grant_app_roles.sql (ALTER DEFAULT PRIVILEGES; re-runs
-- lex-last on every migration job — no explicit GRANT here).

COMMIT;

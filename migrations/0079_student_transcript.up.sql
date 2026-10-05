-- =============================================================================
-- chora-consumption : 0079_student_transcript.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : W6 Slice 1 — StudentTranscript read-model (Four-Mode plan
--                  "Outcome spine"), 2026-07-09
-- Architecture  : internal/domain/student_transcript/* (aggregate + Repository)
--
-- Purpose:
--   Per-learner, event-fed transcript of graded outcomes (assessment
--   submissions + issued certifications), exposed via GET /v1/me/transcript
--   (self, gcid-scoped) and GET /v1/transcript/by-assessments (tenant-scoped,
--   instructor/admin role-gated — the R+ per-offering gradebook join).
--
--   PROJECTION, not a write-model: chora-consumption owns none of this
--   content — it is fed ONLY by verified Pub/Sub events fanned out from the
--   EXISTING LearnerProfile push endpoints (chora.delivery.submission.
--   graded.v1, chora.delivery.certification.issued.v1; see
--   adapter/http/learner_profile_push_handler.go). No new Pub/Sub
--   subscriptions were provisioned for this slice. Cross-DB queries are
--   FORBIDDEN (ddd-enforcement #1).
--
--   Idempotent-upsert identity is (tenant_id, idempotency_key), where
--   idempotency_key is a DETERMINISTIC natural key derived from the source
--   aggregate (e.g. "submission:<id>:graded" / "cert:<id>:issued") — not the
--   volatile event_id — so a true Pub/Sub redelivery AND a legitimate
--   re-grade of the same submission both converge on the SAME row.
--
-- This is an append/upsert-only projection (no learner-facing delete path);
-- soft-delete (ddd-enforcement #5) does not apply. RLS-enabled (every
-- read/write runs rls.ApplySession first). UUID default; UUIDv7 minted in
-- the domain layer.
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS student_transcript_entries (
    entry_id        UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID            NOT NULL,
    gcid            UUID            NOT NULL,
    kind            VARCHAR(16)     NOT NULL
                                     CHECK (kind IN ('assessment', 'certification')),
    source_ref      TEXT            NOT NULL,          -- assessment_id or cert_id; no FK (cross-domain)
    title           TEXT            NOT NULL DEFAULT '',
    score_earned    DOUBLE PRECISION,
    score_possible  DOUBLE PRECISION,
    score_percent   DOUBLE PRECISION,                  -- derived: earned/possible*100 when possible>0
    passed          BOOLEAN,
    course_id       TEXT,                               -- nullable; populated from certification events only
    occurred_at     TIMESTAMPTZ     NOT NULL,
    idempotency_key TEXT            NOT NULL,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now()
);

-- Idempotent-upsert identity: ON CONFLICT (tenant_id, idempotency_key).
CREATE UNIQUE INDEX IF NOT EXISTS uq_student_transcript_entries_idem
    ON student_transcript_entries (tenant_id, idempotency_key);

-- Hot path: GET /v1/me/transcript (self, newest-first).
CREATE INDEX IF NOT EXISTS idx_student_transcript_entries_gcid
    ON student_transcript_entries (tenant_id, gcid, occurred_at DESC);

-- Hot path: GET /v1/transcript/by-assessments?assessment_ids=... (the R+
-- per-offering gradebook join — source_ref = ANY($ids), tenant-scoped).
CREATE INDEX IF NOT EXISTS idx_student_transcript_entries_source_ref
    ON student_transcript_entries (tenant_id, source_ref);

-- -----------------------------------------------------------------------------
-- Row-Level Security — composes with database-level domain isolation.
-- Every read/write runs SET LOCAL chora.tenant_id (rls.ApplySession) BEFORE the
-- user query. Per-learner scoping (ListByGCID) is an explicit gcid predicate,
-- NOT an RLS policy — the by-assessments read intentionally spans every
-- learner in the tenant (role-gated at the HTTP layer, fail-closed).
-- -----------------------------------------------------------------------------
ALTER TABLE student_transcript_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE student_transcript_entries FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON student_transcript_entries;
CREATE POLICY tenant_isolation ON student_transcript_entries
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Grants are handled by 9999_grant_app_roles.sql (re-runs lex-last; ALTER
-- DEFAULT PRIVILEGES grants app_rw / app_ro on new tables automatically).

COMMIT;

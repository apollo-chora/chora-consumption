-- =============================================================================
-- chora-consumption : 0055_weakness_review_pending.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : ADR-205 D4 — Growth-Edge bounded HITL review-loop bridge
--                 (CHO-1973), 2026-07-01
-- Architecture  : docs/architecture/adrs/adr-205-growth-edge-analyser-graduation.md (D4)
--                 chora-contracts/proto/events/consumption/weakness.proto
--                   (WeaknessReviewPending — the review-panel bridge event)
--                 internal/domain/weakness_upload/* (upload aggregate + ReviewPanel)
--
-- Purpose:
--   The graduated (LangGraph) weakness-analyser crew PAUSES at the bounded HITL
--   interrupt (ADR-205 D4) after diagnosing candidate edges but BEFORE persisting
--   anything, and emits chora.consumption.weakness.review_pending.v1 carrying the
--   full review panel across the domain boundary (cross-DB forbidden). This
--   migration lets chora-consumption PARK the upload job at that interrupt:
--     - status gains 'AWAITING_REVIEW' (between ANALYZING and COMPLETED);
--     - review_payload JSONB stores the panel verbatim (proposed edges, candidate
--       struggles, available outputs + their mana prices, the fronting Familiar)
--       so the A+ FE can poll GET .../uploads/{id} and drive the bounded
--       accept/reject/merge decision. The orchestrator owns proposed_edge_id;
--       it rides through the JSONB OPAQUELY (stored + echoed, never minted here).
--
--   Additive + backward-compatible: the CHECK only WIDENS the allowed status set;
--   existing rows keep NULL review_payload. DARK until the WEAKNESS_CREW_MODE=graph
--   cutover — no live job is ever AWAITING_REVIEW on the single-shot path.
--
-- Soft-delete only (deleted_at) per .claude/rules/ddd-enforcement.md §4. RLS is
-- inherited from the table (0047) — no new table, no new policy. review_payload
-- holds ONLY the distilled panel (concept labels + summaries), never the raw
-- document (that is crypto-shred-bound per 0053 / ADR-186).
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- weakness_doc_uploads — widen the status CHECK to admit the bounded-HITL park
-- state, and add the review-panel JSONB column.
-- -----------------------------------------------------------------------------
ALTER TABLE weakness_doc_uploads
    DROP CONSTRAINT IF EXISTS weakness_doc_uploads_status_check;
ALTER TABLE weakness_doc_uploads
    ADD CONSTRAINT weakness_doc_uploads_status_check
    CHECK (status IN ('QUEUED', 'ANALYZING', 'AWAITING_REVIEW', 'COMPLETED', 'FAILED'));

ALTER TABLE weakness_doc_uploads
    ADD COLUMN IF NOT EXISTS review_payload JSONB;  -- bounded HITL panel; present only while AWAITING_REVIEW

-- Grants handled by 9999_grant_app_roles.sql (ALTER DEFAULT PRIVILEGES; re-runs
-- lex-last on every migration job — no explicit GRANT here).

COMMIT;

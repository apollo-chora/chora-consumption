-- =============================================================================
-- chora-consumption : 0118_student_transcript_seen_at.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Track         : UX refactor Phase B, package B6 item 2
-- Date          : 2026-09-02
--
-- Adds the unseen-result flag to the learner's transcript projection.
--
-- WHY
-- The learner's home wants to say "your result is in", and could not: the read
-- model carried a result STATE but nothing distinguished a result the learner
-- had opened from one they had not. So the card could be neither ranked nor
-- dismissed, and a learner who had already read a grade kept being told about
-- it.
--
-- ONE-WAY BY DESIGN
-- seen_at records when the learner FIRST opened the result. The domain refuses
-- to move it (student_transcript.MarkSeen) and the UPDATE below is guarded
-- `AND seen_at IS NULL`, so the two layers agree and neither can silently
-- resurface an old result by re-stamping it. There is deliberately no un-see:
-- a clearable flag would be a preference, and this is a fact.
--
-- NULLABLE, NO BACKFILL
-- Every existing row becomes "not yet opened", which is the honest reading:
-- nothing recorded whether those results were read, and inventing a stamp
-- would assert something we do not know. The visible consequence is a one-off
-- unseen count on first deploy, which is preferable to silently marking a
-- learner's history as read on their behalf.
--
-- NOT NULL IS DELIBERATELY AVOIDED
-- A NOT NULL column here would have to carry a default, and any default is a
-- claim about when the learner read something. The nullable column is the only
-- shape that can represent "we do not know".
--
-- RLS
-- Unchanged. student_transcript_entries already has ENABLE + FORCE ROW LEVEL
-- SECURITY with the tenant_isolation policy (0079), and adding a column does
-- not touch a policy. Per-learner scoping stays an explicit gcid predicate in
-- the adapter, which matters here: the mark-seen write MUST carry it, or one
-- learner could mark another's result read.
--
-- Grants: 9999_grant_app_roles.sql re-runs lex-last and its ALTER DEFAULT
-- PRIVILEGES already covers this table; a new COLUMN inherits the table grant.
-- =============================================================================

BEGIN;

ALTER TABLE student_transcript_entries
    ADD COLUMN IF NOT EXISTS seen_at TIMESTAMPTZ NULL;

COMMENT ON COLUMN student_transcript_entries.seen_at IS
    'When the learner FIRST opened this result; NULL = never opened. One-way: set once, never moved, never cleared (B6 item 2).';

-- Partial index over the unopened rows only.
--
-- The badge query is "how many unseen for this learner", which touches a small
-- and shrinking subset: every row leaves this index the moment it is read. A
-- full index on (tenant_id, gcid, seen_at) would carry every already-read row
-- forever to answer a question that is only ever asked about the unread ones.
CREATE INDEX IF NOT EXISTS idx_student_transcript_entries_unseen
    ON student_transcript_entries (tenant_id, gcid, occurred_at DESC)
    WHERE seen_at IS NULL;

COMMIT;

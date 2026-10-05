-- =============================================================================
-- chora-consumption : 0093_learning_path_provenance.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- ADR      : ADR-233 — study-list provenance + the consumption/distribution
--            boundary (D2 provenance axis, D3 traversal mode, D4 additive
--            re-sync). Spec-001 US5 (FR-030/FR-031), WS-4.
--
-- WHY
-- ---
-- `LearningPath` is ALREADY a derivation (course_id/enrollment_id are populated
-- only for delivery-bootstrapped paths) — it just had no provenance axis. WS-4
-- adds a SECOND source (a chora-creation `Collection`). Without a provenance
-- axis every new source costs a new one-off column, so ADR-233 D2 replaces the
-- naive `source_collection_id` with a polymorphic (source_type, source_id) pair:
-- a third source now costs a CHECK value, not a column.
--
-- ADR-233 D3 lifts a distinction the domain already had in BEHAVIOUR into the
-- TYPE: `current_index` is a LINEAR cursor, but a spaced-repetition study list
-- is scheduled by decay/due off `sm2_states` (daily_dose.go + sm2.go), never by
-- a cursor. traversal_mode makes that explicit; the domain's Advance() REFUSES
-- on a spaced path rather than moving a meaningless integer.
--
-- 🔴 NOT ADDED — deliberately (ADR-233 D1, the load-bearing invariant):
--    NO `visibility` / audience column. chora_consumption models ONE LEARNER'S
--    PRIVATE TRAVERSAL and must never carry a content-distribution surface.
--    Sharing a study list = sharing its SOURCE (creation Collection / delivery
--    Course, D5); progress sharing is a governed projection owned by
--    chora_sharing (D6). A guard test in the domain package enforces this.
--
-- RETAINED — course_id / enrollment_id are NOT dropped. They are the delivery
--    BINDING (an enrollment reference with its own semantics), which is strictly
--    MORE than provenance. Provenance is added alongside, not instead.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- 1) Provenance axis (D2) + traversal mode (D3) + the dedupe anchor.
-- -----------------------------------------------------------------------------
ALTER TABLE learning_paths
  ADD COLUMN IF NOT EXISTS source_type    TEXT NOT NULL DEFAULT 'ad_hoc';
ALTER TABLE learning_paths
  ADD COLUMN IF NOT EXISTS source_id      UUID NULL;
ALTER TABLE learning_paths
  ADD COLUMN IF NOT EXISTS traversal_mode TEXT NOT NULL DEFAULT 'linear';

-- study_list_event_id — the DURABLE delivery-dedupe anchor for the
-- collection→study-list conversion. The subscriber dedupes on THIS (a durable
-- domain anchor), not on an in-process event-id tracker: a tracker burns its
-- key before the write is durable, so a transient failure swallows the Pub/Sub
-- redelivery and strands the conversion forever (the EnrollmentCreatedSubscriber
-- R1 lesson). NULL for course-bootstrapped + ad-hoc paths.
ALTER TABLE learning_paths
  ADD COLUMN IF NOT EXISTS study_list_event_id UUID NULL;

-- CHECK constraints added separately + idempotently (ADD CONSTRAINT has no
-- IF NOT EXISTS in PostgreSQL).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'learning_paths'::regclass
       AND conname  = 'learning_paths_source_type_check'
  ) THEN
    ALTER TABLE learning_paths
      ADD CONSTRAINT learning_paths_source_type_check
      CHECK (source_type IN ('course','collection','ad_hoc'));
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'learning_paths'::regclass
       AND conname  = 'learning_paths_traversal_mode_check'
  ) THEN
    ALTER TABLE learning_paths
      ADD CONSTRAINT learning_paths_traversal_mode_check
      CHECK (traversal_mode IN ('linear','spaced'));
  END IF;
END $$;

-- -----------------------------------------------------------------------------
-- 2) Backfill: every existing course-bound path is course-provenanced.
--
-- ⚠ RLS: a plain cross-tenant backfill UPDATE silently affects 0 ROWS when the
-- table is FORCE ROW LEVEL SECURITY (even the owner is row-filtered, and
-- `current_setting('chora.tenant_id', true)` is NULL in a migration session →
-- the policy predicate is NULL → no row matches → a SILENT no-op that ships
-- green). The repo idiom (0046 / 0057 / 0061 / 0074) toggles NO FORCE around
-- the UPDATE and restores immediately, atomically within the transaction.
--
-- VERIFIED at authoring time (2026-07-14): `learning_paths` is ENABLE-only
-- (0001_initial.sql:74 — `ENABLE ROW LEVEL SECURITY`, never FORCEd), so the
-- migrate role (the table OWNER) bypasses RLS and a plain UPDATE reaches every
-- tenant. The toggle below is therefore a guarded NO-OP today — but it reads
-- the LIVE `pg_class.relforcerowsecurity` and only toggles when the table is
-- ACTUALLY forced, so (a) it never silently no-ops if someone FORCEs the table
-- later, and (b) it never silently *enables* FORCE on a table that did not have
-- it (a blind NO FORCE → UPDATE → FORCE toggle would do exactly that, changing
-- the table's security posture as a side effect of a data migration).
--
-- The RAISE NOTICE makes the affected row count auditable instead of silent.
-- -----------------------------------------------------------------------------
DO $$
DECLARE
  was_forced BOOLEAN;
  n_rows     BIGINT;
BEGIN
  SELECT relforcerowsecurity INTO was_forced
    FROM pg_class WHERE oid = 'learning_paths'::regclass;

  IF was_forced THEN
    EXECUTE 'ALTER TABLE learning_paths NO FORCE ROW LEVEL SECURITY';
  END IF;

  UPDATE learning_paths
     SET source_type = 'course',
         source_id   = course_id
   WHERE course_id IS NOT NULL
     AND source_type = 'ad_hoc';   -- re-run safe: only touches un-provenanced rows

  GET DIAGNOSTICS n_rows = ROW_COUNT;

  IF was_forced THEN
    EXECUTE 'ALTER TABLE learning_paths FORCE ROW LEVEL SECURITY';
  END IF;

  RAISE NOTICE '0093 backfill: % course-bound path(s) stamped source_type=course (force_rls_was=%)',
    n_rows, was_forced;
END $$;

-- -----------------------------------------------------------------------------
-- 3) Uniqueness.
-- -----------------------------------------------------------------------------

-- One study-list path per (tenant, owner, source collection). Re-converting the
-- SAME collection must find + additively re-sync the SAME path (ADR-233 D4),
-- never mint a second one. Partial: course-bound + ad-hoc paths are unaffected
-- (they may share a NULL/dup source_id space).
CREATE UNIQUE INDEX IF NOT EXISTS idx_learning_paths_source_collection_owner
  ON learning_paths (tenant_id, owner_gcid, source_id)
  WHERE source_type = 'collection' AND deleted_at IS NULL;

-- The delivery-dedupe anchor. A redelivered convert event carries the SAME
-- study_list_event_id, so this index is the durable guard that a duplicate push
-- can never mint a second path. NOT filtered on deleted_at: a soft-deleted path
-- must still hold its anchor, otherwise a redelivery after the learner deleted
-- the study list would silently RESURRECT it as a new row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_learning_paths_study_list_event
  ON learning_paths (study_list_event_id)
  WHERE study_list_event_id IS NOT NULL;

-- Provenance lookup (the subscriber's get-or-create probe).
CREATE INDEX IF NOT EXISTS idx_learning_paths_source
  ON learning_paths (tenant_id, source_type, source_id)
  WHERE deleted_at IS NULL;

COMMENT ON COLUMN learning_paths.source_type IS
  'ADR-233 D2 — polymorphic provenance: course | collection | ad_hoc. A third source costs a CHECK value, not a column.';
COMMENT ON COLUMN learning_paths.source_id IS
  'ADR-233 D2 — the upstream aggregate id (course_id | collection_id | NULL). course_id/enrollment_id are RETAINED as the delivery binding.';
COMMENT ON COLUMN learning_paths.traversal_mode IS
  'ADR-233 D3 — linear (current_index cursor advances) | spaced (cursor INERT; SM-2/Ebbinghaus schedules off sm2_states; Advance() refuses).';
COMMENT ON COLUMN learning_paths.study_list_event_id IS
  'ADR-233 — durable dedupe anchor for chora.creation.collection.converted_to_study_list.v1. The subscriber dedupes on THIS, not on an in-process event-id tracker.';

COMMIT;

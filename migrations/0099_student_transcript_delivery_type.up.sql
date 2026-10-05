-- =============================================================================
-- chora-consumption : 0099_student_transcript_delivery_type.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-2224 — §10.6 capstone criterion 1 (mode attribution)
-- Date          : 2026-07-16
-- ADR           : NOT an ADR-190 change. ADR-190 D1 explicitly scopes the
--                 transcript model to Consumption/W6, OUTSIDE ADR-190:
--                 "The transcript model itself is a Consumption/W6 concern;
--                 ADR-190 fixes only the Delivery-side outcome-event contract."
--
-- Purpose:
--   Let the unified transcript attribute a delivery MODE to a graded outcome.
--
--   §10.6's capstone criterion is "one learner's unified transcript rolls up
--   graded components from >=2 MODES". It could not be met, and not for want of
--   data plumbing: student_transcript_entries carries
--   kind CHECK IN ('assessment','certification') and NOTHING mode-bearing. A
--   graduate assessment and a short-course grade BOTH arrive on
--   chora.delivery.submission.graded.v1 and BOTH project as kind='assessment',
--   so the transcript could evidence 2 KINDS and never 2 MODES.
--
--   delivery_type is the missing axis. It is ORTHOGONAL to `kind`, never a
--   widening of it: `kind` says WHAT the outcome is, delivery_type says WHICH
--   MODE delivered it. A certification issued off a graduate offering is both.
--   Widening the kind CHECK to carry modes would have collapsed two independent
--   facts into one column and lost one of them.
--
-- Source of the value: chora_delivery.offerings.delivery_type
-- ({graduate|short|async}; ADR-190 D1 puts delivery_type on the Offering so one
-- Course stays reusable across modes). It reaches us ONLY as a snapshot on
-- deliveryv1.SubmissionGraded field 14, resolved at grade time. Cross-DB reads
-- are FORBIDDEN (ddd-enforcement #1) and the Offering aggregate publishes no
-- events at all, so the event field is the only channel that exists.
--
-- -----------------------------------------------------------------------------
-- NULLABLE, and deliberately NO CHECK CONSTRAINT
-- -----------------------------------------------------------------------------
-- NULL = genuinely UNATTRIBUTABLE, never a default:
--   * chora_delivery.assessments.offering_id is NULLABLE (its migration 0033) —
--     a freestanding assessment has no Offering and therefore no mode; and
--   * every graded event published before field 14 existed carries nothing.
-- Backfilling a guess (say 'graduate') would write a fact into a learner's
-- academic record that nothing in the system supports. So: no backfill.
--
-- NO CHECK is a deliberate divergence from the sibling `kind` CHECK, and the
-- reason is ownership, not laziness:
--   * `kind` is minted HERE — consumption controls its value set, so a CHECK is
--     safe and can only catch our own bug.
--   * delivery_type is minted in ANOTHER domain. A CHECK on a foreign-controlled
--     value is a fail-CLOSED cross-domain coupling: the day chora_delivery ships
--     a 4th mode, every INSERT carrying it would raise 23514 -> the projector
--     errors -> NACK -> redeliver -> DEAD-LETTER, and the learner would lose a
--     real GRADE over a metadata label.
-- The upstream itself models it this way: offerings.delivery_type is bare
-- TEXT NOT NULL with NO CHECK (its migration 0031), validated in the delivery
-- DOMAIN via DeliveryType.IsValid(). We mirror that exactly — the value set is
-- enforced in student_transcript.DeliveryType.Valid(), the projector NORMALISES
-- an unrecognised value to NULL and WARNS loudly, and the grade always lands.
-- (Also per the standing pg-as-validator rule: parse at the boundary, do not
-- make Postgres the validator of someone else's vocabulary.)
--
-- -----------------------------------------------------------------------------
-- No index, and no new grant
-- -----------------------------------------------------------------------------
-- NO INDEX: the two hot reads are keyed (tenant_id, gcid, occurred_at DESC) and
-- (tenant_id, source_ref). delivery_type is a DISPLAY attribute nobody filters
-- or facets on; an index for a predicate that does not exist is dead weight on
-- every write. Add one when a filtered read actually ships.
--
-- NO GRANT NEEDED: 9999_grant_app_roles.sql grants at TABLE level
-- ("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public"), and a
-- table-level privilege covers columns added later. The 9999-skipping trap
-- (targeted apply -> app_rw with zero privileges -> 42501) bites a hand-applied
-- CREATE TABLE, which introduces a brand-new ungranted object. This is an
-- ALTER on an already-granted table, so it is safe to apply targeted.
--
-- ROW LEVEL SECURITY: unchanged. The tenant_isolation policy from 0079 still
-- applies; adding a column changes no policy and needs none.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS) per the migration-runner fail-fast
-- lesson.
-- =============================================================================

BEGIN;

ALTER TABLE student_transcript_entries
    ADD COLUMN IF NOT EXISTS delivery_type TEXT;

COMMENT ON COLUMN student_transcript_entries.delivery_type IS
    'Delivery MODE of the parent Offering ({graduate|short|async}), snapshotted '
    'at grade time from deliveryv1.SubmissionGraded field 14 (CHO-2224). '
    'Orthogonal to kind: kind = WHAT the outcome is, delivery_type = WHICH mode '
    'delivered it. NULL = genuinely unattributable (a freestanding assessment '
    'has no Offering; certification.issued.v1 carries no mode; pre-field-14 '
    'events carry nothing) — never a default. Deliberately UNCHECKED: the value '
    'set is owned by chora_delivery and enforced in the consumption domain, so '
    'a new upstream mode degrades to NULL instead of dead-lettering a grade.';

COMMIT;

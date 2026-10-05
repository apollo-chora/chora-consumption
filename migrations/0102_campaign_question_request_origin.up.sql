-- =============================================================================
-- chora-consumption : 0102_campaign_question_request_origin.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : CHO-2298: ADR-227 D13 campaign qgen daily-budget split
-- Date          : 2026-07-18
--
-- Purpose:
--   Split the campaign qgen daily budget by request ORIGIN so an explicit
--   "Practice this hex" tap is never starved by the automatic dose-march.
--
--   Before this, campaign_question_sets carried ONE daily cap (1 request per
--   learner per UTC day across ALL nodes AND both entrances). Once the
--   automatic dose-march spent that budget, an explicit hex tap on any
--   un-generated node returned an empty miss ("prepares on its next march")
--   until the next UTC day, the CHO-2298 incident.
--
--   request_origin is the axis that lets the daily-cap count bill the march
--   feeder and the explicit tap to SEPARATE budgets (march 1/day, tap 3/day).
--   It is stamped at request time by the sole requester (maybeRequestCampaignGen)
--   alongside requested_on; an idle / retrieval-cache row carries the 'march'
--   default and is never counted (requested_on IS NULL).
--
-- -----------------------------------------------------------------------------
-- NOT NULL DEFAULT 'march', and a CHECK, unlike 0099's foreign-owned column
-- -----------------------------------------------------------------------------
-- The value set is minted HERE (consumption owns 'march'|'tap'), so a CHECK is
-- safe: it can only catch our own bug, never dead-letter a cross-domain event
-- (contrast 0099_student_transcript_delivery_type, whose value set is owned by
-- chora_delivery and is therefore deliberately UNCHECKED). Existing rows
-- backfill to 'march', the conservative default that preserves the pre-split
-- behaviour for the automatic feeder and is the correct attribution for every
-- historical request (the tap lane did not exist before this migration).
--
-- NO INDEX: the one hot read (the daily-cap count) is keyed
-- (tenant_id, learner_gcid, requested_on) and adds request_origin as a cheap
-- residual filter over the <= (march+tap) rows a learner requests per day. An
-- index for so few rows is dead weight on every write; add one if a filtered
-- read ever ships.
--
-- NO GRANT NEEDED: 9999_grant_app_roles.sql grants at TABLE level, which covers
-- columns added later; this is an ALTER on the already-granted 0079 table, so a
-- targeted apply does not hit the 9999-skipping 42501 trap.
--
-- ROW LEVEL SECURITY: unchanged. The 0079 tenant_isolation policy still
-- applies; adding a column changes no policy and needs none.
--
-- Idempotent (ADD COLUMN IF NOT EXISTS; the inline CHECK rides the same guard).
-- =============================================================================

BEGIN;

ALTER TABLE campaign_question_sets
    ADD COLUMN IF NOT EXISTS request_origin TEXT NOT NULL DEFAULT 'march'
        CHECK (request_origin IN ('march', 'tap'));

COMMENT ON COLUMN campaign_question_sets.request_origin IS
    'WHICH entrance requested a generation: march (automatic daily dose-march '
    'feeder) or tap (explicit "Practice this hex" tap). The daily qgen budget '
    'is split per origin (march 1/day, tap 3/day) so an explicit tap is never '
    'starved by the automatic march. Stamped at request time alongside '
    'requested_on; an idle / retrieval-cache row carries the march default and '
    'is never counted (requested_on IS NULL). ADR-227 D13, CHO-2298.';

COMMIT;

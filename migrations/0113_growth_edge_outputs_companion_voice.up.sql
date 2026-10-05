-- =============================================================================
-- chora-consumption : 0113_growth_edge_outputs_companion_voice.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Story         : ADR-254 D4 (weakness.outputs_generated.v1 gains output kind
--                 companion_voice: the diagnosis crew's Companion voice step)
-- Date          : 2026-08-23
--
-- Purpose:
--   The growth_edge_outputs.kind CHECK (0105) admits study_aids and
--   practice_test only. The kennel's diagnosis crew now also emits the
--   Companion's spoken-register reflection as kind 'companion_voice' on the
--   same event; the projection (weakness_outputs_generated_subscriber) refuses
--   an unknown kind at the domain boundary, and the DB CHECK would refuse it
--   at the INSERT. Widen both in lockstep (growth_edge_output.KindCompanionVoice
--   lands with this file).
--
-- Data-not-schema otherwise: no rows change; the constraint is recreated with
-- the wider set (DROP CONSTRAINT + ADD CONSTRAINT is not data-destroying).
-- =============================================================================

BEGIN;

ALTER TABLE growth_edge_outputs DROP CONSTRAINT IF EXISTS growth_edge_outputs_kind_check;
ALTER TABLE growth_edge_outputs
    ADD CONSTRAINT growth_edge_outputs_kind_check
    CHECK (kind IN ('study_aids', 'practice_test', 'companion_voice'));

COMMIT;

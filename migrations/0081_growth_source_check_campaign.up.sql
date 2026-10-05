-- =============================================================================
-- chora-consumption : 0081_growth_source_check_campaign.up.sql
--
-- ADR             : ADR-227 D10 — campaign conquest XP (WS-C5, CHO-2084)
-- Related         : ADR-149 §EXP economy (0032 seeded the ledger CHECK) ·
--                   ADR-218 D6 (one EXP economy — curve.go + identity 0036
--                   carry the same four tokens).
-- Domain          : Content Consumption  /  Database : chora_consumption
-- Date            : 2026-07-09
--
-- Purpose:
--   0032's familiar_growth_events.source CHECK is the defensive
--   belt-and-suspenders behind the app-layer canonical enum. WS-C5 adds four
--   campaign conquest sources (identity exp_source_def migration 0036 +
--   consumption growth/curve.go, parity-pinned); without this widening every
--   campaign award insert dies 23514 at the belt (proven live 2026-07-09:
--   campaign-xp push NACKs) even though the app layer accepts the token.
--
--   Idempotent as a pair: DROP IF EXISTS + ADD (re-apply safe).
-- =============================================================================

BEGIN;

ALTER TABLE familiar_growth_events
    DROP CONSTRAINT IF EXISTS familiar_growth_events_source_check;

ALTER TABLE familiar_growth_events
    ADD CONSTRAINT familiar_growth_events_source_check CHECK (source IN (
        'atom_session',
        'ebbinghaus_review',
        'hex_expand',
        'conv_turn',
        'daily_dose_open',
        'social_share',
        'social_reaction',
        'junction_accepted',
        'atom_authored',
        'admin_grant',
        'hatch_roll',
        -- ADR-227 D10 / WS-C5 campaign conquest vocabulary (verified events
        -- only — fed exclusively by the domain-emitted campaign.* topics).
        'campaign_rung_cleared',
        'campaign_rung_refreshed',
        'campaign_node_won',
        'campaign_goal_sealed'
    ));

COMMIT;

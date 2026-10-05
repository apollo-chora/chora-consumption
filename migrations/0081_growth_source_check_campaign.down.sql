-- =============================================================================
-- chora-consumption : 0081_growth_source_check_campaign.down.sql
--
-- Reverts 0081: re-narrows familiar_growth_events.source to the 0032 legacy
-- token list. Re-added NOT VALID so already-awarded campaign ledger rows
-- (append-only audit history — never deleted, ADR-203) survive the revert
-- while NEW campaign-source inserts are refused again.
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
        'hatch_roll'
    )) NOT VALID;

COMMIT;

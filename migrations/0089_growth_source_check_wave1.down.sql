-- =============================================================================
-- chora-consumption : 0089_growth_source_check_wave1.down.sql
-- =============================================================================
-- Re-narrow to the 0081 vocabulary. NOT VALID so historical Wave-1 rows never
-- block the down (matching the 0081 down idiom); new inserts re-constrain.
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
        'campaign_rung_cleared',
        'campaign_rung_refreshed',
        'campaign_node_won',
        'campaign_goal_sealed'
    )) NOT VALID;

COMMIT;

-- =============================================================================
-- chora-consumption : 0089_growth_source_check_wave1.up.sql
-- =============================================================================
-- CHO-2090 (F-I3, ADR-228 D4 Wave 1): widen the familiar_growth_events.source
-- CHECK for the three Wave-1 verified XP sources. Without this every Wave-1
-- award dies 23514 at insert time (exactly how WS-C5 first failed live —
-- 0081's lesson). Mirrors the 0081 idiom: drop + re-add with the full
-- vocabulary (the CHECK is defensive; curve.go + identity exp_source_def
-- 0030/0036/0037 stay the semantic sources of truth).
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
        -- ADR-227 D10 / WS-C5 campaign conquest vocabulary.
        'campaign_rung_cleared',
        'campaign_rung_refreshed',
        'campaign_node_won',
        'campaign_goal_sealed',
        -- ADR-228 D4 Wave 1 (CHO-2090): verified cross-surface sources.
        'weakness_grown',
        'submission_graded',
        'module_completed'
    ));

COMMIT;

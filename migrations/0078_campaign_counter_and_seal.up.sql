-- chora-consumption : 0078_campaign_counter_and_seal.up.sql
-- WS-C1 of ADR-227 (CHO-2080) — the two state columns the campaign domain
-- core needs that 0077 did not carry.
--
--  * campaign_node_progress.current_rung_correct — the D6 rung-clear rule
--    (CAMPAIGN_RUNG_CLEAR_CORRECT server-graded corrects AT the current
--    rung, default 2, tunable) needs a counter that persists across
--    sessions and days. Wrong answers never reset it — the retention
--    penalty is the Ebbinghaus side's job and the anti-farm defence is
--    calendar time (D7), not counter resets. Reset to 0 on each rung clear.
--
--  * goals.campaign_sealed_at — campaign seal HISTORY (D3). Deliberately
--    DISTINCT from personal_completed_at: the bare ADR-213 personal-axis
--    PATCH can set that flag without any campaign, so re-seal gating
--    (>= N newly-won nodes since last seal + the weekly cap) must read its
--    own timestamp. Never cleared (seal history is fact); ReopenPersonal
--    does not touch it.
--
-- Additive only; both columns default cleanly — no backfill, so no NO-FORCE
-- toggle txn. Grants ride 9999_grant_app_roles.sql (ALTER DEFAULT
-- PRIVILEGES); RLS policies are row-level and unaffected by new columns.

BEGIN;

ALTER TABLE campaign_node_progress
    ADD COLUMN IF NOT EXISTS current_rung_correct SMALLINT NOT NULL DEFAULT 0
        CONSTRAINT campaign_progress_counter_nonneg CHECK (current_rung_correct >= 0);

COMMENT ON COLUMN campaign_node_progress.current_rung_correct IS
    'ADR-227 D6: server-graded corrects at the CURRENT (next uncleared) rung since it became current; clears to 0 on rung clear; never reset by a wrong answer';

ALTER TABLE goals
    ADD COLUMN IF NOT EXISTS campaign_sealed_at TIMESTAMPTZ;

COMMENT ON COLUMN goals.campaign_sealed_at IS
    'ADR-227 D3 campaign seal history (latest seal moment; re-seal gate clock). Distinct from personal_completed_at (bare ADR-213 declaration). Never cleared.';

COMMIT;

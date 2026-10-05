-- 0113_growth_edge_outputs_companion_voice.down.sql: narrows the kind CHECK back
-- to the 0105 set. Any companion_voice rows must be gone first (the re-add
-- validates existing rows and fails loud otherwise; do not DELETE them here,
-- they are learner artifacts).
BEGIN;
ALTER TABLE growth_edge_outputs DROP CONSTRAINT IF EXISTS growth_edge_outputs_kind_check;
ALTER TABLE growth_edge_outputs
    ADD CONSTRAINT growth_edge_outputs_kind_check
    CHECK (kind IN ('study_aids', 'practice_test'));
COMMIT;

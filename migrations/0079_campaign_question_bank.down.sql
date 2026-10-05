-- chora-consumption : 0079_campaign_question_bank.down.sql
-- Reverses 0079: drops the WS-C3 campaign question bank and the atom_index
-- cognitive_level projection column (its values re-hydrate from creation
-- atom events after a re-up, so the drop loses nothing durable).

BEGIN;

DROP TABLE IF EXISTS campaign_question_sets;

ALTER TABLE atom_index
    DROP COLUMN IF EXISTS cognitive_level;

COMMIT;

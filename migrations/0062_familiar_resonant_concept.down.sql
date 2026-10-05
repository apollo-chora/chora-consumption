-- 0062 down: drop the awakening resonant-concept pick column (R3-1, CHO-2013).
ALTER TABLE familiar_instances
    DROP COLUMN IF EXISTS resonant_concept_id;

-- 0101_familiar_reveal_split.down.sql — drop the CHO-2229 reveal column.
-- Reverting re-fuses the roll into the hatch commit; the deployed service
-- must be rolled back alongside (its CommitReveal/CommitHatch SQL reads
-- this column).

BEGIN;

ALTER TABLE familiar_instances
  DROP COLUMN IF EXISTS revealed_at;

COMMIT;

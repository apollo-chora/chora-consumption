-- 0085 down: re-dark the fog_scout scout Skill (P2 wave C). Reverses the ADR-174
-- activation flip — invoke/equip then 409s SKILL_NOT_ACTIVE again. Owned grants
-- are untouched (ownership accrues regardless of active). Symmetric with the 0061
-- dark seed.
UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('fog_scout')
   AND deleted_at IS NULL;

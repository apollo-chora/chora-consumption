-- 0082 down: re-dark the two answerable Skills (CHO-2016 P2 wave B). Reverses the
-- ADR-174 activation flip — invoke/equip then 409s SKILL_NOT_ACTIVE again. Owned
-- grants are untouched (ownership accrues regardless of active). Symmetric with
-- the 0075 dark-hold.
UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('quiz_me', 'socratic_drill')
   AND deleted_at IS NULL;

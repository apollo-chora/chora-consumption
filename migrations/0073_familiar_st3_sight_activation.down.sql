-- 0073 down: re-dark the two st3 sight Skills (CHO-2014 wave A). Reverses the
-- ADR-174 activation flip — equip then 409s SKILL_NOT_ACTIVE again. Owned grants
-- are untouched (ownership accrues regardless of active).
UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('weakness_sight', 'map_sight')
   AND deleted_at IS NULL;

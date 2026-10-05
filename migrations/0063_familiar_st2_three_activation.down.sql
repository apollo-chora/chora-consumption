-- 0063 down: re-dark the st2 three (CHO-2013 P1.B, R4-1). Reverses the
-- ADR-174 activation flip — equip then 409s SKILL_NOT_ACTIVE again. Owned
-- grants are untouched (ownership accrues regardless of active).
UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('explain_anew', 'recap_scribe', 'progress_mirror')
   AND deleted_at IS NULL;

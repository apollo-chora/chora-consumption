-- 0075 down: NO activation on rollback. 0075's up HOLDS quiz_me + socratic_drill
-- DARK (active=FALSE) — it never activated them — so rolling it back leaves them
-- dark exactly as the 0061 seed left them. Re-asserted here as an explicit,
-- idempotent, symmetric reverse (never a flip to active=TRUE).
UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('quiz_me', 'socratic_drill')
   AND deleted_at IS NULL;

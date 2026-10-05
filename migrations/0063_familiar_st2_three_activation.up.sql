-- =============================================================================
-- chora-consumption : 0063_familiar_st2_three_activation.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Growth & Grimoire CR P1.B "activate the st2 three"
--            (CHO-2013, R4-1 ruling 2026-07-03; spec pack §5 P1-delta #4 +
--            §8 ADR-174 eval gate). The ADR-174 per-Skill eval suites
--            (services/chora-ai-kernel-orchestrator/eval/gate/golden_sets/
--            familiar_skills/{key}/ + eval-policy-familiar.yaml) cleared for
--            explain_anew / recap_scribe / progress_mirror, so their
--            catalogue release gate flips active=TRUE.
--
-- Activation is DATA, not schema (spec §5 P1-delta #4): the 0061 seed stays
-- dark for all 27 rows and THIS migration is a standalone UPDATE, so the
-- 0061 seed-drift gate is untouched. The UPDATE list is pinned to
-- seedspec.P1ActivationThree by TestMigration0063_ActivatesExactlyP1Three.
--
-- reminder_bell (the fourth st2-floor Skill and a Launch-Seven member) stays
-- owned-dark until P2 — its notify.schedule tool + autonomy-adjacent eval
-- suite are not built yet (R4-1). Equip requires active=TRUE, so a dark
-- reminder_bell grant accrues but cannot be equipped (409 SKILL_NOT_ACTIVE).
--
-- Idempotent: re-running is a no-op on already-active rows. Reversible via
-- the .down.sql (flips the three back dark).
-- =============================================================================

UPDATE familiar_skill_catalog
   SET active = TRUE
 WHERE skill_key IN ('explain_anew', 'recap_scribe', 'progress_mirror')
   AND deleted_at IS NULL;

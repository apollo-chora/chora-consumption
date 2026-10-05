-- =============================================================================
-- chora-consumption : 0082_familiar_answerable_activation_flip.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Growth & Grimoire — quiz_me + socratic_drill "answerable
--            pipe" (CHO-2016, P2 wave B) RELEASE. This is the follow-up flip
--            that 0075's header promised: 0075 held these two DARK pending their
--            ADR-174 §8 answerable eval; that gate has now PASSED (18d re-eval,
--            2026-07-09, live temp-activate→drive→revert):
--
--              quiz_me         facts_groundedness 1.0   / safety 1.0 / IF 4.273
--              socratic_drill  facts_groundedness 0.909 / safety 1.0 / IF 4.091
--              adversarial block-rate 1.0
--
--            Both clear the §8 floors (facts_groundedness ≥ 0.80, safety = 1.0,
--            instruction_following ≥ 3.5), so the eval-gated flip is authorised.
--            The P2 socraticFramingInstruction anchor fix (ef299287e) is what
--            moved socratic from 0.75 BLOCK → 0.909 PASS.
--
--            Deliberately EXACTLY seedspec.P2AnswerableActivationTwo. fog_scout
--            (the third P2 remainder) stays dark pending its suggestion-inbox
--            write builder + eval — smuggling it in would breach the §8 gate.
--
-- Activation is DATA, not schema (spec §5 P1-delta #4): a standalone UPDATE, the
-- dark seed (0061) + the 0075 hold untouched. The UPDATE list is pinned to
-- seedspec.P2AnswerableActivationTwo by TestMigration0082_ActivatesExactlyP2AnswerableTwo.
--
-- Idempotent: re-running is a no-op on already-active rows. Reversible via the
-- .down.sql (flips the two back dark → equip 409s SKILL_NOT_ACTIVE).
-- =============================================================================

UPDATE familiar_skill_catalog
   SET active = TRUE
 WHERE skill_key IN ('quiz_me', 'socratic_drill')
   AND deleted_at IS NULL;

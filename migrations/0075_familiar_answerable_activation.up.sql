-- =============================================================================
-- chora-consumption : 0075_familiar_answerable_activation.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Growth & Grimoire — quiz_me + socratic_drill "answerable
--            pipe" (CHO-2016), the SECOND P2 activation wave (wave B). Their
--            invoke-runner builders (buildQuizMeTurn / buildSocraticDrillTurn,
--            RETRIEVE mode only), deterministic server-side item picks
--            (weak / concept_ref / due), and the result_kind/items[] response
--            seam have landed.
--
--            BUT their ADR-174 §8 answerable eval suite has NOT yet passed
--            (owner-driven), so this migration deliberately HOLDS THEM DARK. It
--            mirrors 0073's activation-vehicle STRUCTURE (a standalone data
--            UPDATE on familiar_skill_catalog, idempotent, reversible,
--            drift-tested) but it does NOT flip active=TRUE — releasing an
--            un-evaluated LLM Skill past the §8 eval gate is exactly what the
--            gate forbids (the same gating that held the sight skills dark until
--            0073, and reminder_bell dark at P1).
--
--            When the owner's answerable eval gate passes, a FOLLOW-UP migration
--            flips these two active=TRUE (mirroring 0073). NOT this one.
--
-- Activation is DATA, not schema (spec §5 P1-delta #4). This UPDATE re-asserts
-- the two answerable rows DARK (active=FALSE) — a belt-and-braces guard at this
-- point in migration history, and the durable record of the wave's release
-- contract. The DARK-hold list is pinned to seedspec.P2AnswerableActivationTwo by
-- TestMigration0075_HoldsAnswerableDark.
--
-- Idempotent: re-running is a no-op (the 0061 seed already left them dark).
-- Reversible via .down.sql (a symmetric dark-hold — 0075 never activated them).
-- =============================================================================

UPDATE familiar_skill_catalog
   SET active = FALSE
 WHERE skill_key IN ('quiz_me', 'socratic_drill')
   AND deleted_at IS NULL;

-- =============================================================================
-- chora-consumption : 0073_familiar_st3_sight_activation.up.sql
--
-- Domain   : Content Consumption (5 core)
-- Database : chora_consumption
-- Context  : Familiar Growth & Grimoire CR P2 "Fledgling's Kit" (CHO-2014) —
--            FIRST activation wave (wave A). The two st3 "sight" Skills whose
--            invoke-runner builders (buildWeaknessSightTurn / buildMapSightTurn),
--            backing readers (weakness.read / kg.read_map, server-side), and
--            ADR-174 honest-empty/gap eval suites have landed flip active=TRUE.
--            Both are SinkChat narration Skills — no answerable pipe required.
--
-- Deliberately a SUBSET of seedspec.P2ActivationFive: quiz_me + socratic_drill
-- stay dark pending the answerable quiz-pipe (C); fog_scout stays dark pending
-- its suggestion-inbox write builder. Releasing them now would ship tool-less /
-- un-evaluated Skills past the §8 ADR-174 eval gate (same gating that held
-- reminder_bell dark at P1).
--
-- Activation is DATA, not schema (spec §5 P1-delta #4): the dark seed is
-- untouched and THIS migration is a standalone UPDATE. The UPDATE list is pinned
-- to seedspec.P2SightActivationTwo by TestMigration0073_ActivatesExactlyP2SightTwo.
--
-- Idempotent: re-running is a no-op on already-active rows. Reversible via the
-- .down.sql (flips the two back dark → equip 409s SKILL_NOT_ACTIVE).
-- =============================================================================

UPDATE familiar_skill_catalog
   SET active = TRUE
 WHERE skill_key IN ('weakness_sight', 'map_sight')
   AND deleted_at IS NULL;

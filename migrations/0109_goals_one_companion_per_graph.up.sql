-- chora-consumption : 0109_goals_one_companion_per_graph.up.sql
-- CHO-2403: a Companion belongs to at most ONE knowledge graph.
--
-- Domain   : Content Consumption (Team 1), chora_consumption
-- Spec     : ADR-204 (learner-owned Goal); owner ruling 2026-08-20
--
-- WHY THIS IS NOT ALREADY TRUE. The bond was described as "1:1" in three
-- places (the 0051 column comment, the goals PATCH docstring, and the
-- goal.ErrAlreadyAttached message), and all three mean GOAL to FAMILIAR: a
-- goal holds at most one Companion. Goal.AttachFamiliar enforces exactly
-- that, by checking the goal's own slot. Nothing checked the other
-- direction, so one Companion could be attached to any number of goals, and
-- the roster picker offered already-assigned Companions because it had no
-- reason not to. The invariant everyone believed existed did not.
--
-- WHAT LANDS HERE. A partial unique index over the LIVE, bonded rows:
--
--   * scoped by tenant_id, because a cross-tenant collision must never be
--     able to make one tenant's write fail on another tenant's row;
--   * WHERE attached_familiar_id IS NOT NULL, so the many unbonded goals do
--     not collide with each other on NULL;
--   * WHERE deleted_at IS NULL, so soft-deleting a map genuinely FREES its
--     Companion instead of holding it hostage forever. This matters: goals
--     are soft-deleted, never hard-deleted (ddd-enforcement #5), so without
--     this predicate a learner could delete a map and then be permanently
--     unable to re-home the Companion that was on it.
--
-- This is the GUARANTEE. The 409 in goals_handler.go is the courtesy: it
-- gives the learner a reason instead of letting a 23505 surface as a 500.
-- Both exist on purpose, per the owner's ruling to build all three layers.
--
-- FAILS LOUD ON DIRTY DATA, BY DESIGN. If any learner already holds one
-- Companion on two live goals, this index cannot be created and the
-- migration aborts. That is correct: the repair is to decide WHICH graph
-- keeps the Companion, and silently detaching the loser here would destroy a
-- learner's binding without them asking. Resolve the duplicates, then re-run.
--
-- Idioms: partial unique index mirroring idx_goals_learner (0051), which is
-- likewise partial on deleted_at IS NULL. Grants ride
-- 9999_grant_app_roles.sql; an index needs none of its own.

BEGIN;

CREATE UNIQUE INDEX IF NOT EXISTS ux_goals_attached_familiar_live
    ON goals (tenant_id, attached_familiar_id)
    WHERE attached_familiar_id IS NOT NULL AND deleted_at IS NULL;

COMMIT;

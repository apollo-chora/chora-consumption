-- =============================================================================
-- chora-consumption : 0052_goal_mastered_concept_count.up.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : §3 Goal graduation (event-driven half) — CHO-1962, 2026-06-29
-- Architecture  : docs/architecture/adrs/adr-204-knowledge-graph-goal-first-micro-effort-surface.md §9
--                 internal/domain/goal/* (Goal.MasteredConceptCount field)
--
-- Purpose:
--   The goal-graduation subscriber (consumes chora.consumption.weakness.grown.v1)
--   recomputes, per active Goal, how many of its concept_set members the learner
--   has mastered. To avoid re-emitting a goal.progress_updated.v1 event on a
--   redelivered weakness.grown, it persists the last-emitted count here and
--   compares the freshly derived count against it. This is the subscriber's
--   high-water mark — NOT the read-time progress (the %-ring derives that live in
--   goals_handler.go and never trusts this column).
--
--   Inherits the goals table's RLS (0051: ENABLE + FORCE + tenant_isolation FOR
--   ALL) — no new policy. NOT NULL DEFAULT 0 so existing rows backfill to "nothing
--   mastered yet" without a separate UPDATE.
-- =============================================================================

BEGIN;

ALTER TABLE goals
    ADD COLUMN IF NOT EXISTS mastered_concept_count int NOT NULL DEFAULT 0;

COMMIT;

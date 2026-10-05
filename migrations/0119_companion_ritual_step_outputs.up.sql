-- =============================================================================
-- chora-consumption : 0119_companion_ritual_step_outputs.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Track         : UX refactor Phase D, package D2 (S8)
-- ADR           : ADR-257 section 6
-- Date          : 2026-09-02
--
-- Persists what each Ritual step produced, so a run can be explained after it
-- finishes.
--
-- WHY
-- Step outputs were an in-memory buffer: the runner appended each step's output,
-- handed the whole array once to the declared sink on completion, and dropped it
-- with the stack frame. For the chat sink the durable write was a reference
-- string and nothing else. So "what did step 2 actually say" was unanswerable
-- the moment a run ended, and the learner run story could honestly report only
-- what ran, in what order, whether it finished, and what it cost.
--
-- Worse, a failed or blocked step returned early and discarded the outputs of
-- every step that HAD completed. Those are exactly what a learner needs in order
-- to see where a run stopped: a run that says "stopped early" and shows nothing
-- is indistinguishable from one that never started.
--
-- ONE COLUMN, NOT A CHILD TABLE
-- A separate companion_ritual_step_outputs table was considered and rejected in
-- the ADR. The runner writes the whole array once, at terminate, so a child
-- table buys no write pattern; it would add a second surface to the closure map
-- and to the RLS estate for nothing. A JSONB column parallel to decision_stamp
-- keeps the audit fact atomic on one row.
--
-- NOT NULL DEFAULT '[]'
-- Deliberate, and the opposite of the nullable choice 0118 made for seen_at.
-- There, NULL carried the real meaning "we do not know whether this was read".
-- Here there is nothing to not know: a run either produced step outputs or
-- produced none, and an empty array says "none" exactly. A nullable column would
-- add a third state that no reader could interpret, and the domain already
-- normalises a nil slice to an empty one before it persists.
--
-- BOUNDED IN THE DOMAIN, NOT IN THE SCHEMA
-- The size bound is a per-step and a per-run rune cap in the domain
-- (StepOutputRuneCap, RunOutputRuneCap), with truncation marked in the stored
-- entry. A CHECK on the JSONB length would be a second, differently-worded
-- bound that could disagree with the domain's, and the failure mode of that
-- disagreement is a run that executes, costs mana, and then cannot be written
-- down. The domain truncates BEFORE the write, so the write always succeeds.
--
-- WIRE SHAPE
-- The entries are snake_case (step_index, step_id, skill_key, text, truncated).
-- The sibling companion_ritual_revisions.steps column keys SkillKey and Params
-- by their GO FIELD NAMES and cannot be retagged without silently emptying every
-- stored revision, so the two adjacent JSONB columns differ ON PURPOSE. A Go
-- test pins this one (TestStepOutput_WireShapeIsSnakeCaseAndPinned).
--
-- RLS
-- Unchanged. companion_ritual_runs already carries ENABLE plus FORCE ROW LEVEL
-- SECURITY with the tenant_isolation policy on chora.tenant_id (0071, renamed by
-- 0110), and adding a column does not touch a policy. No RLS-bypass surface is
-- opened: the four sanctioned surfaces (ADR-165/184/192/255) are untouched.
--
-- CLOSURE
-- The row is already pseudonymised through owner_gcid like every other column on
-- this table, so closure needs no new key. The chora_consumption
-- PII_Closure_Map.yaml names the column explicitly (ADR-257 section 6 clause 6),
-- because a stored step output is learner-visible text and a closure map that
-- does not name it would be a map that lies.
--
-- NO INDEX
-- Nothing queries into this column. It is read only alongside the row it sits
-- on, by run id or by the existing (tenant_id, companion_id, started_at DESC)
-- history index. A GIN index here would cost every write to buy no read.
--
-- Grants: 9999_grant_app_roles.sql re-runs lex-last and a new COLUMN inherits
-- the table grant.
-- =============================================================================

BEGIN;

ALTER TABLE companion_ritual_runs
    ADD COLUMN IF NOT EXISTS step_outputs JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN companion_ritual_runs.step_outputs IS
    'S8 (ADR-257 section 6): per-step outputs in step order, keyed by step_id where the revision has one and positionally otherwise. Bounded and truncation-marked in the domain before the write. snake_case keys, unlike the sibling revisions.steps column which is pinned to Go field names. Learner-visible through the ADR-215 D5 projection; prompt version, prompt hash and raw tool ids stay in decision_stamp and stay auditor-only. Closure pseudonymises via owner_gcid like every other column on this row.';

COMMIT;

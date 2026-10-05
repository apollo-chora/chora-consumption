-- chora-consumption : 0095_goal_knowledge_silent_status.up.sql
-- CHO-2180 — an honestly-declined reflection must TERMINATE.
--
-- Domain : Content Consumption (Team 1) — chora_consumption
-- Amends : 0092 (familiar_goal_knowledge, CHO-2118)
--
-- THE BUG. The synthesis crew is allowed to decline: when the Companion has
-- nothing TRUE to say about a goal it returns NO_MEMORY_YET and refuses to invent
-- a memory. That instinct is right. But it expressed the decline by publishing
-- NOTHING — which is indistinguishable, from chora-consumption's side, from a
-- synthesis that crashed or was lost. So the cache row kept requested_at set and
-- generated_at NULL, i.e. "never generated", which NeedsSynthesis retries
-- UNCONDITIONALLY. The result, live:
--
--   * the learner sat on a "Reflecting on what it knows…" marker that could
--     never resolve, because nothing was ever coming; and
--   * every tab-open past the 15-minute claim window re-bought the SAME
--     guaranteed-to-decline Gemini call, forever.
--
-- THE FIX. A decline is a RESULT, so it now travels like one: the crew publishes
-- goal_knowledge.synthesized.v1 with nothing_to_say=true (both goal_knowledge
-- topics are schema-registry-free, so the field is additive without a revision),
-- and consumption records it via RecordNoReflection — carrying the full ADR-197
-- decision stamp (silence is a MODEL DECISION; a row that cannot name the prompt
-- and model that concluded it is exactly as unexplainable as an unattributable
-- reflection), stamping generated_at, and releasing the claim.
--
-- This migration adds the terminal state that write needs.
--
--   'silent' = the synthesis RAN and concluded there is nothing true to say yet.
--
-- It is an ANSWER, not a waiting state. The read path serves it as the honest
-- "no reflection yet" (ADR-207 — unknown is a state, never fabricate), and it is
-- never retried on a timer: the only thing that can change "there is nothing to
-- say about this goal" is a change to what the model LOOKED AT, and every such
-- change (memory, weakness, progress, chat turn, eviction, re-root) already
-- arrives as an invalidation. Re-running max-age over an unmoved view would spend
-- a model call to be told the same thing again.
--
-- Rows stranded by the bug SELF-HEAL with no data fix: they still read as
-- never-generated, so the next tab-open requests one final synthesis, the crew
-- declines, and the decline is now recorded as terminal.
--
-- ALTER TYPE ... ADD VALUE is transaction-safe on PG12+ so long as the new value
-- is not USED in the same transaction. This migration only adds it; the first
-- write of 'silent' comes later, from the application.

ALTER TYPE familiar_goal_knowledge_status ADD VALUE IF NOT EXISTS 'silent';

COMMENT ON COLUMN familiar_goal_knowledge.status IS
    'Cache lifecycle. pending = never generated, or a synthesis is in flight. fresh = synthesised, not invalidated, carrying text. stale = invalidated but KEEPS its last good text (serve-stale-while-regen). silent (CHO-2180) = the synthesis RAN and honestly concluded there is nothing true to say about this goal yet — a terminal ANSWER, served to the learner as "no reflection yet" and never retried on a timer, only when an invalidation proves the inputs moved. A silent row keeps saying silent while a fresh synthesis is claimed, exactly as a stale row keeps serving its last good text.';

COMMENT ON COLUMN familiar_goal_knowledge.synthesis_text IS
    'The last good reflection. NULL/empty before the first synthesis, and empty on a silent row (the model declined — never fabricate one to fill the space). NEVER blanked by an invalidation: an invalidated row keeps its text so the learner sees the previous reflection marked "reflecting…" rather than a blank. A later synthesis that concludes silent DOES supersede prose, deliberately — the model looked at the current view and declined to stand behind it.';

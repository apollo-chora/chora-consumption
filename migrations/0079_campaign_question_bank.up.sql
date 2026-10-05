-- chora-consumption : 0079_campaign_question_bank.up.sql
-- WS-C3 of ADR-227 (the Familiar Campaign) — CHO-2082: the retrieval-first
-- question lane + qgen gap-fill (D13) needs two additive pieces:
--
--  * atom_index.cognitive_level — the retrieval post-filter substrate.
--    Retrieval (ContentRetrieval.SearchEmbeddings) cannot filter by level
--    (addendum #4), so the drillcache idiom post-filters candidates on the
--    LOCAL projection. Values are the atom-domain ORIGINAL-Bloom lowercase
--    labels (knowledge | comprehension | application | analysis |
--    evaluation | synthesis — the campaign Rung.Label() vocabulary,
--    addendum #4's two-vocabulary rule: FE maps revised-Bloom labels).
--    NULL = level unknown (rows projected before this migration; events
--    hydrate on atom create/publish/revise) — an unknown level NEVER
--    level-matches, so the lane honestly falls through to gap generation.
--    No backfill (cross-DB reads forbidden), hence no NO-FORCE toggle.
--
--  * campaign_question_sets — ADR-202 §4's per-learner QuestionBank is
--    UNBUILT (addendum #4), so generated ladder questions persist here as
--    per-user rows (proofing_tests 0068 idiom): one live row per
--    (tenant, learner, concept, rung) carrying BOTH the level-matched
--    retrieval cache (retrieved_atom_ids — refreshed opportunistically at
--    compose time) and the qgen lane state (idle → requested → ready |
--    failed; assist_id keys the SHARED ai_assist terminal topics exactly
--    like proofing_tests.assist_id; questions_payload stores the
--    BatchCandidatePayload JSON verbatim, served for free on refreshers
--    and re-climbs — "generate once, retrieve forever"). requested_on
--    backs the D13 generation bound (≤2 questions/learner/UTC-day ⇒ at
--    most one 2-question request per day, counted per learner across
--    nodes). The lane is mana-EXEMPT (D13) — no reservation columns.
--
-- Idioms: 0056 RLS (ENABLE + FORCE + tenant_isolation on chora.tenant_id;
-- per-learner scoping is an explicit query predicate in Go); UUIDv7 minted
-- in the Go domain layer (gen_random_uuid() is only the DB-side fallback);
-- soft-delete; live-row partial indexes; grants come from
-- 9999_grant_app_roles.sql via ALTER DEFAULT PRIVILEGES (no inline grant).
-- Additive only; no backfill.

BEGIN;

ALTER TABLE atom_index
    ADD COLUMN IF NOT EXISTS cognitive_level TEXT;

COMMENT ON COLUMN atom_index.cognitive_level IS
    'ADR-227 D13/WS-C3: atom CognitiveLevel projected from creation atom events (f22), ORIGINAL-Bloom lowercase labels (knowledge..synthesis). NULL = unknown (pre-0079 rows; hydrates on create/publish/revise) and never level-matches the campaign retrieval post-filter.';

CREATE TABLE IF NOT EXISTS campaign_question_sets (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID        NOT NULL,
    learner_gcid         UUID        NOT NULL,
    concept_id           UUID        NOT NULL,   -- concept_nodes.id; same-DB cross-aggregate ref, no FK (merge/split soft-deletes nodes)
    concept_key          TEXT        NOT NULL,   -- minted-once slug (retention/weakness vocabulary; addendum #2)
    rung                 SMALLINT    NOT NULL,   -- 1..6 ORIGINAL-Bloom ladder position (D6)
    retrieved_atom_ids   TEXT[]      NOT NULL DEFAULT '{}',  -- level-matched retrieval cache (drillcache idiom)
    retrieval_checked_at TIMESTAMPTZ,                        -- when the cache was last resolved
    generation_status    TEXT        NOT NULL DEFAULT 'idle',
    assist_id            TEXT,                                -- keys the SHARED ai_assist terminal topics (proofing idiom)
    requested_on         DATE,                                -- D13 daily generation bound (per-learner count)
    questions_payload    JSONB,                               -- BatchCandidatePayload verbatim (reuse = zero LLM)
    failure_reason       TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ,                         -- soft-delete (#5)
    CONSTRAINT campaign_qsets_rung_range   CHECK (rung BETWEEN 1 AND 6),
    CONSTRAINT campaign_qsets_status_enum  CHECK (generation_status IN ('idle', 'requested', 'ready', 'failed')),
    -- A ready row MUST carry its payload — never a fabricated ready (AC4).
    CONSTRAINT campaign_qsets_ready_payload CHECK (generation_status <> 'ready' OR questions_payload IS NOT NULL),
    -- A requested row MUST carry its assist key + request day (terminal
    -- resolution + daily-cap accounting both depend on them).
    CONSTRAINT campaign_qsets_requested_keys CHECK (
        generation_status <> 'requested' OR (assist_id IS NOT NULL AND requested_on IS NOT NULL)
    )
);

-- One live question-set per (tenant, learner, concept, rung).
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_question_sets_live
    ON campaign_question_sets (tenant_id, learner_gcid, concept_id, rung)
    WHERE deleted_at IS NULL;

-- Terminal events resolve by assist_id (SHARED topic — unknown ids ack-skip).
CREATE UNIQUE INDEX IF NOT EXISTS uq_campaign_question_sets_assist
    ON campaign_question_sets (tenant_id, assist_id)
    WHERE assist_id IS NOT NULL AND deleted_at IS NULL;

-- Daily-cap count: how many generation requests this learner made today.
CREATE INDEX IF NOT EXISTS idx_campaign_question_sets_requested_on
    ON campaign_question_sets (tenant_id, learner_gcid, requested_on)
    WHERE requested_on IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE campaign_question_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign_question_sets FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON campaign_question_sets;
CREATE POLICY tenant_isolation ON campaign_question_sets
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMENT ON TABLE campaign_question_sets IS
    'ADR-227 D13 (WS-C3, CHO-2082): per-(tenant, learner, concept, rung) campaign question lane — level-matched retrieval cache + qgen gap-fill state (idle/requested/ready/failed; assist_id keys the shared ai_assist terminal topics; questions_payload = BatchCandidatePayload verbatim, reused forever). Mana-exempt lane; ≤2 generated questions/learner/day via requested_on.';

COMMIT;

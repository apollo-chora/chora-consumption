-- 0094_familiar_memory_source_metadata.up.sql
-- CHO-2179 (ADR-231 D4) — a grounded memory note must persist WHERE IT CAME FROM.
--
-- Until now a `memory_type='research'` row (the web_research Seeker's sink) held
-- the familiar's prose and NOTHING else: no citations, no queries. The Vertex
-- grounding-api-redirect links that backed it EXPIRE (~30 days, ADR-231 D4), so
-- once they died the note could never explain itself — not to the learner, not to
-- an auditor (IMDA D2). grounded.Hit's own doc comment already ASSERTED the
-- opposite ("the web_research memory-note sink persists domain+title+snippet");
-- this migration makes that true.
--
-- Shape of the payload:
--   {
--     "web_search_queries": ["shape of the earth", "oblate spheroid"],
--     "citations": [{"domain":"nasa.gov","title":"…","snippet":"…"}]
--   }
--
-- ⚠ NOTE WHAT IS *NOT* IN IT: the citation `uri`. The redirect link expires, and a
-- dead link still LOOKS like a working source — worse than no link. domain+title+
-- snippet and the issued queries are plain text that never expires, so they are the
-- durable record. The uri stays a live-response convenience only.
--
-- ⚠ AND NOT IN content_text: that column is what gets vector-embedded, so folding
-- provenance into the prose would poison recall — and would read to the learner as
-- knowledge, which transparency metadata explicitly is not.
--
-- ADDITIVE + NULLABLE: every existing row (chat_turn / recap / ceremony /
-- weakness_diagnosis) is untouched and stays NULL. No backfill — a note written
-- before this migration genuinely has no recoverable provenance, and inventing one
-- would be a lie. NULL is the honest "we did not record it" state.
--
-- RLS: familiar_memory_recall already has ENABLE + FORCE ROW LEVEL SECURITY with
-- the `tenant_isolation` policy FOR ALL (mig 0045) — a policy on the table covers
-- every column, so a new column needs no policy change. Writes continue to go
-- through rls.ApplySession inside RunInTx.

ALTER TABLE familiar_memory_recall
    ADD COLUMN IF NOT EXISTS source_metadata JSONB;

COMMENT ON COLUMN familiar_memory_recall.source_metadata IS
    'CHO-2179 / ADR-231 D4 — durable grounded provenance for Seeker notes (memory_type=''research''): {"web_search_queries":[...],"citations":[{"domain","title","snippet"}]}. The Vertex grounding-api-redirect uri is deliberately NOT persisted: it expires (~30d), so a note storing it would rot to dead links. NULL for every non-grounded memory_type.';

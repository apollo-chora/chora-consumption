-- 0076_familiar_persona.up.sql — ADR-219 D2 (CHO-2015): the learner Persona sheet.
--
-- Adds the fenced free-text guidance note + a monotonic persona version counter
-- to familiar_instances. The typed persona knobs (tone / hint policy /
-- difficulty cap / citation strictness / language / address style / interest
-- chips / archetype) continue to live in the existing configured_rules JSONB
-- (the agent already composes from it — no new column needed for those).
--
-- Purely additive: NOT-NULL with DEFAULTs, so existing rows backfill to the
-- leak-free empty/zero state and the agent's mapConfiguredRules behaviour is
-- byte-identical until a learner edits their persona. No cross-tenant UPDATE,
-- so no FORCE-RLS backfill toggle is needed.
ALTER TABLE familiar_instances
  ADD COLUMN IF NOT EXISTS guidance_note   TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS persona_version INT  NOT NULL DEFAULT 0;

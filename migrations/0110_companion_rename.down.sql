-- =============================================================================
-- chora-consumption : 0110_companion_rename.down.sql
--
-- Reverses 0110_companion_rename.up.sql EXACTLY by replaying its audit log
-- (companion_rename_0110_log) in reverse order: every renamed constraint /
-- index / trigger / function / relation / column / type / enum value / policy
-- goes back to its recorded old name, every rewritten function body, CHECK
-- definition and comment goes back to its recorded old text. The stored-value
-- updates are inverted explicitly (they are value maps, not catalog objects).
-- Finally the log table itself is dropped.
--
-- Run only with the pre-rename service binary (the renamed service reads the
-- companion_* schema). One transaction; fails loud.
-- =============================================================================

BEGIN;

SET LOCAL lock_timeout = '30s';

-- ----------------------------------------------------------------------------
-- 1) Stored VALUES back (inverse of up §3; order does not matter between them)
-- ----------------------------------------------------------------------------
-- Lift FORCE RLS for the inverse rewrites below, for exactly the reason the up
-- migration does (see 0110 up §2b): these run as the table OWNER, and under
-- FORCE ROW LEVEL SECURITY with no tenant GUC an owner UPDATE matches ZERO
-- rows and silently no-ops. Without this the DOWN migration reports success
-- while reverting nothing, which is worse than failing: it would leave
-- companion-valued data behind a schema that has been renamed back.
-- Restored below, in this same transaction, before the catalog names revert.
CREATE TEMP TABLE _0110_forced_down ON COMMIT DROP AS
SELECT c.oid::regclass::text AS rel
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relforcerowsecurity
   AND c.relname = ANY (ARRAY[
         'concept_nodes',
         'concept_edges',
         'companion_skill_catalog',
         'species_paths',
         'companion_ritual_revisions',
         'companion_ritual_runs',
         'companion_memory_recall',
         'weakness_doc_uploads',
         'outbox_events',
         'idempotency_keys'
       ]);

DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT rel FROM _0110_forced_down ORDER BY rel LOOP
    EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', r.rel);
  END LOOP;
END $$;

UPDATE companion_skill_catalog
   SET skill_key = 'fog_scout',
       price_key = 'familiar_skill_fog_scout',
       name      = 'Fog Scout'
 WHERE skill_key = 'kg_explore';

UPDATE companion_skill_catalog
   SET price_key = 'familiar_skill_' || substr(price_key, length('companion_skill_') + 1)
 WHERE price_key LIKE 'companion\_skill\_%';

UPDATE species_paths
   SET entries = replace(entries::text, '"kg_explore"', '"fog_scout"')::jsonb
 WHERE entries::text LIKE '%"kg_explore"%';

UPDATE companion_ritual_revisions
   SET steps = replace(steps::text, '"kg_explore"', '"fog_scout"')::jsonb
 WHERE steps::text LIKE '%"kg_explore"%';

UPDATE companion_ritual_runs
   SET decision_stamp = replace(decision_stamp::text, '"kg_explore"', '"fog_scout"')::jsonb
 WHERE decision_stamp::text LIKE '%"kg_explore"%';

UPDATE companion_memory_recall
   SET scope_key = 'familiar:' || substr(scope_key, length('companion:') + 1)
 WHERE scope_key LIKE 'companion:%';

UPDATE weakness_doc_uploads
   SET review_payload = replace(replace(review_payload::text, '"companion_id":', '"familiar_id":'), '"companion":', '"familiar":')::jsonb
 WHERE review_payload::text LIKE '%"companion%';

UPDATE outbox_events
   SET topic          = replace(topic, 'companion', 'familiar'),
       event_type     = replace(event_type, 'companion', 'familiar'),
       aggregate_type = replace(aggregate_type, 'companion', 'familiar')
 WHERE status IN ('pending', 'failed')
   AND (topic LIKE '%companion%' OR event_type LIKE '%companion%' OR aggregate_type LIKE '%companion%');

UPDATE idempotency_keys
   SET key = replace(key, 'companion', 'familiar')
 WHERE key LIKE '%companion%';

-- ----------------------------------------------------------------------------
-- 2) CHECK constraints: drop the companion definitions, restore the provenance
--    values, re-add the recorded old definitions (validated on re-add).
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT ident FROM companion_rename_0110_log WHERE kind = 'check' ORDER BY id DESC
  LOOP
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', split_part(r.ident, '.', 1), split_part(r.ident, '.', 2));
  END LOOP;
END $$;

UPDATE concept_nodes SET provenance = 'familiar_suggested_accepted' WHERE provenance = 'companion_suggested_accepted';
UPDATE concept_edges SET provenance = 'familiar_suggested_accepted' WHERE provenance = 'companion_suggested_accepted';

-- Restore FORCE RLS on exactly the tables lifted above, and refuse to commit
-- if any was missed: a table left owner-readable is a tenant-isolation
-- regression, and a down migration must not be the thing that ships one.
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT rel FROM _0110_forced_down ORDER BY rel LOOP
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', r.rel);
  END LOOP;
END $$;

DO $$
DECLARE
  n int;
BEGIN
  SELECT count(*) INTO n
    FROM _0110_forced_down f
    JOIN pg_class c ON c.oid = f.rel::regclass
   WHERE NOT c.relforcerowsecurity;
  IF n > 0 THEN
    RAISE EXCEPTION '0110 down: % table(s) left without FORCE ROW LEVEL SECURITY', n;
  END IF;
END $$;

DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT ident, old_value FROM companion_rename_0110_log WHERE kind = 'check' ORDER BY id DESC
  LOOP
    EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s', split_part(r.ident, '.', 1), split_part(r.ident, '.', 2), r.old_value);
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 3) Catalog names, bodies and comments back, newest change first
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT id, kind, ident, old_value, new_value
      FROM companion_rename_0110_log
     WHERE kind <> 'check'
     ORDER BY id DESC
  LOOP
    CASE r.kind
      WHEN 'comment_column' THEN
        EXECUTE format('COMMENT ON COLUMN %I.%I IS %L', split_part(r.ident, '.', 1), split_part(r.ident, '.', 2), r.old_value);
      WHEN 'comment_i', 'comment_I' THEN
        EXECUTE format('COMMENT ON INDEX %I IS %L', r.ident, r.old_value);
      WHEN 'comment_v' THEN
        EXECUTE format('COMMENT ON VIEW %I IS %L', r.ident, r.old_value);
      WHEN 'comment_m' THEN
        EXECUTE format('COMMENT ON MATERIALIZED VIEW %I IS %L', r.ident, r.old_value);
      WHEN 'comment_S' THEN
        EXECUTE format('COMMENT ON SEQUENCE %I IS %L', r.ident, r.old_value);
      WHEN 'comment_r', 'comment_p' THEN
        EXECUTE format('COMMENT ON TABLE %I IS %L', r.ident, r.old_value);
      WHEN 'policy' THEN
        EXECUTE format('ALTER POLICY %I ON %s RENAME TO %I', r.new_value, r.ident, r.old_value);
      WHEN 'enumvalue' THEN
        EXECUTE format('ALTER TYPE %I RENAME VALUE %L TO %L', r.ident, r.new_value, r.old_value);
      WHEN 'type' THEN
        EXECUTE format('ALTER TYPE %I RENAME TO %I', r.new_value, r.old_value);
      WHEN 'column' THEN
        EXECUTE format('ALTER TABLE %I RENAME COLUMN %I TO %I', r.ident, r.new_value, r.old_value);
      WHEN 'relation' THEN
        CASE r.ident
          WHEN 'S' THEN EXECUTE format('ALTER SEQUENCE %I RENAME TO %I', r.new_value, r.old_value);
          WHEN 'v' THEN EXECUTE format('ALTER VIEW %I RENAME TO %I', r.new_value, r.old_value);
          WHEN 'm' THEN EXECUTE format('ALTER MATERIALIZED VIEW %I RENAME TO %I', r.new_value, r.old_value);
          ELSE          EXECUTE format('ALTER TABLE %I RENAME TO %I', r.new_value, r.old_value);
        END CASE;
      WHEN 'functiondef' THEN
        EXECUTE r.old_value;
      WHEN 'function' THEN
        EXECUTE format('ALTER FUNCTION %I(%s) RENAME TO %I', r.new_value, r.ident, r.old_value);
      WHEN 'trigger' THEN
        EXECUTE format('ALTER TRIGGER %I ON %s RENAME TO %I', r.new_value, r.ident, r.old_value);
      WHEN 'index' THEN
        EXECUTE format('ALTER INDEX %I RENAME TO %I', r.new_value, r.old_value);
      WHEN 'constraint' THEN
        EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I', r.ident, r.new_value, r.old_value);
      ELSE
        RAISE EXCEPTION '0110_companion_rename down: unknown log kind % (id %)', r.kind, r.id;
    END CASE;
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 4) Assert the familiar names are back, then drop the log
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  leftovers text;
BEGIN
  SELECT string_agg(x, ', ') INTO leftovers FROM (
    SELECT 'relation ' || relname AS x FROM pg_class
     WHERE relnamespace = 'public'::regnamespace AND relname ILIKE '%companion%'
       AND relname NOT LIKE 'companion\_rename\_0110\_log%'  -- the log, its sequence and its pkey (dropped below)
    UNION ALL
    SELECT 'column ' || c.relname || '.' || a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
     WHERE c.relnamespace = 'public'::regnamespace AND a.attnum > 0 AND NOT a.attisdropped AND a.attname ILIKE '%companion%'
    UNION ALL
    SELECT 'function ' || proname FROM pg_proc WHERE pronamespace = 'public'::regnamespace AND proname ILIKE '%companion%'
  ) t;
  IF leftovers IS NOT NULL THEN
    RAISE EXCEPTION '0110_companion_rename down: companion leftovers after restore: %', leftovers;
  END IF;
END $$;

DROP TABLE companion_rename_0110_log;

COMMIT;

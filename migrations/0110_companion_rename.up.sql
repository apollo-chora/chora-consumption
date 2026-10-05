-- =============================================================================
-- chora-consumption : 0110_companion_rename.up.sql
--
-- Domain        : Content Consumption (core)
-- Database      : chora_consumption
-- Story         : ADR-254 D9 (Learning Companion rename; Familiar -> Companion)
-- Date          : 2026-08-23
--
-- Purpose:
--   Rename every LIVE database object, stored value and comment that still
--   speaks "familiar" to the Companion vocabulary the renamed service expects
--   (services/chora-consumption reads companion_* tables, companion_id columns,
--   'kg_explore' / 'companion_skill_*' catalogue keys, 'companion:' memory scope
--   keys, 'companion_suggested_accepted' provenance, the 'companion' card
--   archetype, chora.consumption.companion.* outbox topics).
--
-- Why a CATALOG-DRIVEN rename and not re-authored history:
--   Migrations 0001..0109 stay byte-identical to what the live lane applied. The
--   runner's tracker (chora_runner_schema_migrations) is keyed on FILENAME and
--   its backdated gate refuses a renamed pending file, so renaming the applied
--   files would wedge the lane; re-authoring their content would be a lie about
--   history. A fresh database therefore speaks familiar through 0109 and
--   companion from this file on: "0001..0109 then 0110" is the schema the Go
--   mirror (seedspec) describes today.
--
-- What it does (one transaction; every step fails loud, nothing is skipped):
--   1. renames constraints, indexes, triggers, functions, tables/views/
--      sequences, columns, enum types, enum values and policies whose NAME
--      contains "familiar" (familiar -> companion, same casing), and rewrites
--      the one function body that spells the old table name;
--   2. recreates the CHECK constraints whose DEFINITION carries a familiar
--      value (concept_nodes / concept_edges provenance) around the value update;
--   3. updates stored VALUES: companion_skill_catalog skill_key fog_scout ->
--      kg_explore and price_key familiar_skill_* -> companion_skill_*,
--      species_paths / ritual steps / ritual run stamps "fog_scout" ->
--      "kg_explore", companion_memory_recall scope_key 'familiar:' ->
--      'companion:', concept provenance familiar_suggested_accepted ->
--      companion_suggested_accepted, weakness_doc_uploads.review_payload JSON
--      keys familiar/familiar_id -> companion/companion_id, PENDING/FAILED
--      outbox_events topic/event_type/aggregate_type familiar -> companion,
--      idempotency_keys.key familiar -> companion (so a redelivery across the
--      cut is still recognised), object comments;
--   4. asserts that NOTHING named or defined with "familiar" remains (a miss is
--      a migration failure, not a warning).
--
-- Every catalog change is recorded in companion_rename_0110_log (kind, ident,
-- old_value, new_value) so the down migration restores EXACTLY what was changed
-- (comments and function bodies included) instead of guessing by inverse
-- substitution. The log stays as the audit record of the rename.
--
-- Also changed here: the kg_explore Skill's learner-facing display name
--   ('Fog Scout' -> 'Knowledge Explorer', owner ruling 2026-08-23 on the ADR-254
--   copy row "fog" -> "explore / uncharted").
--
-- NOT changed here (deliberately):
--   - the Skill FAMILY value 'companion' (chk_skill_catalog_family): it predates
--     the rename and was never a Familiar name;
--   - published / dead-lettered outbox rows and the runner's own tracker rows:
--     history;
--   - payload bytes of the few pending outbox rows (encoded before the cut).
--
-- Applied by the runner lane like any other file (psql -f, ON_ERROR_STOP=1);
-- the BEGIN/COMMIT below makes it atomic. Grants follow the renamed objects
-- (ACLs are per-OID); 9999_grant_app_roles.sql re-grants anyway.
-- =============================================================================

BEGIN;

SET LOCAL lock_timeout = '30s';

-- ----------------------------------------------------------------------------
-- 0) Undo/audit log
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS companion_rename_0110_log (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL,
    ident      TEXT NOT NULL,
    old_value  TEXT NOT NULL,
    new_value  TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE companion_rename_0110_log IS
  'ADR-254 D9 catalog rename record (0110_companion_rename): one row per renamed/rewritten catalog object, replayed in reverse by the down migration.';

-- ----------------------------------------------------------------------------
-- 1) Catalog NAMES (familiar -> companion, same casing)
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  r        record;
  newname  text;
  olddef   text;
  newdef   text;
BEGIN
  -- 1a. table constraints (renaming a PK/UNIQUE constraint renames its index)
  FOR r IN
    SELECT c.conname, c.conrelid::regclass::text AS rel
      FROM pg_constraint c
     WHERE c.connamespace = 'public'::regnamespace
       AND c.conrelid <> 0
       AND c.conname ILIKE '%familiar%'
     ORDER BY c.conrelid, c.conname
  LOOP
    newname := replace(replace(replace(r.conname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I', r.rel, r.conname, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('constraint', r.rel, r.conname, newname);
  END LOOP;

  -- 1b. remaining indexes
  FOR r IN
    SELECT c.relname
      FROM pg_class c
     WHERE c.relnamespace = 'public'::regnamespace
       AND c.relkind IN ('i', 'I')
       AND c.relname ILIKE '%familiar%'
     ORDER BY c.relname
  LOOP
    newname := replace(replace(replace(r.relname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER INDEX %I RENAME TO %I', r.relname, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('index', '', r.relname, newname);
  END LOOP;

  -- 1c. triggers
  FOR r IN
    SELECT t.tgname, t.tgrelid::regclass::text AS rel
      FROM pg_trigger t
      JOIN pg_class c ON c.oid = t.tgrelid
     WHERE NOT t.tgisinternal
       AND c.relnamespace = 'public'::regnamespace
       AND t.tgname ILIKE '%familiar%'
     ORDER BY t.tgrelid, t.tgname
  LOOP
    newname := replace(replace(replace(r.tgname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER TRIGGER %I ON %s RENAME TO %I', r.tgname, r.rel, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('trigger', r.rel, r.tgname, newname);
  END LOOP;

  -- 1d. functions: name, then body (pg_get_functiondef of the renamed function,
  --     familiar -> companion, re-executed as CREATE OR REPLACE)
  FOR r IN
    SELECT p.oid, p.proname, pg_get_function_identity_arguments(p.oid) AS args
      FROM pg_proc p
     WHERE p.pronamespace = 'public'::regnamespace
       AND p.proname ILIKE '%familiar%'
     ORDER BY p.proname
  LOOP
    newname := replace(replace(replace(r.proname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER FUNCTION %I(%s) RENAME TO %I', r.proname, r.args, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('function', r.args, r.proname, newname);
  END LOOP;
  FOR r IN
    SELECT p.oid, p.proname, pg_get_function_identity_arguments(p.oid) AS args, pg_get_functiondef(p.oid) AS def
      FROM pg_proc p
     WHERE p.pronamespace = 'public'::regnamespace
       AND p.prokind = 'f'
       AND p.prosrc ILIKE '%familiar%'
     ORDER BY p.proname
  LOOP
    olddef := r.def;
    newdef := replace(replace(replace(olddef, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE newdef;
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('functiondef', r.proname || '(' || r.args || ')', olddef, newdef);
  END LOOP;

  -- 1e. tables / partitioned tables / views / materialized views / sequences
  FOR r IN
    SELECT c.relname, c.relkind::text AS relkind
      FROM pg_class c
     WHERE c.relnamespace = 'public'::regnamespace
       AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
       AND c.relname ILIKE '%familiar%'
     ORDER BY c.relname
  LOOP
    newname := replace(replace(replace(r.relname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    CASE r.relkind
      WHEN 'S' THEN EXECUTE format('ALTER SEQUENCE %I RENAME TO %I', r.relname, newname);
      WHEN 'v' THEN EXECUTE format('ALTER VIEW %I RENAME TO %I', r.relname, newname);
      WHEN 'm' THEN EXECUTE format('ALTER MATERIALIZED VIEW %I RENAME TO %I', r.relname, newname);
      ELSE          EXECUTE format('ALTER TABLE %I RENAME TO %I', r.relname, newname);
    END CASE;
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('relation', r.relkind, r.relname, newname);
  END LOOP;

  -- 1f. columns (after the relation renames: ident = the NEW relation name).
  --     Index attributes are included: an index keeps the column name it was
  --     built with, so a renamed table column leaves "familiar_id" behind in
  --     pg_attribute of every index on it; ALTER TABLE <index> RENAME COLUMN
  --     is how PostgreSQL renames those (no ALTER INDEX form exists).
  FOR r IN
    SELECT c.relname, c.relkind::text AS relkind, a.attname
      FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
     WHERE c.relnamespace = 'public'::regnamespace
       AND c.relkind IN ('r', 'p', 'v', 'm', 'i', 'I')
       AND a.attnum > 0
       AND NOT a.attisdropped
       AND a.attname ILIKE '%familiar%'
     ORDER BY c.relname, a.attnum
  LOOP
    newname := replace(replace(replace(r.attname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    CASE r.relkind
      WHEN 'v' THEN EXECUTE format('ALTER VIEW %I RENAME COLUMN %I TO %I', r.relname, r.attname, newname);
      WHEN 'm' THEN EXECUTE format('ALTER MATERIALIZED VIEW %I RENAME COLUMN %I TO %I', r.relname, r.attname, newname);
      ELSE          EXECUTE format('ALTER TABLE %I RENAME COLUMN %I TO %I', r.relname, r.attname, newname);
    END CASE;
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('column', r.relname, r.attname, newname);
  END LOOP;

  -- 1g. enum + domain types (composite row types followed their tables in 1e)
  FOR r IN
    SELECT t.typname
      FROM pg_type t
     WHERE t.typnamespace = 'public'::regnamespace
       AND t.typtype IN ('e', 'd')
       AND t.typname ILIKE '%familiar%'
     ORDER BY t.typname
  LOOP
    newname := replace(replace(replace(r.typname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER TYPE %I RENAME TO %I', r.typname, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('type', '', r.typname, newname);
  END LOOP;

  -- 1h. enum VALUES (card_archetype 'familiar' -> 'companion'; rows follow the label)
  FOR r IN
    SELECT t.typname, e.enumlabel
      FROM pg_enum e
      JOIN pg_type t ON t.oid = e.enumtypid
     WHERE t.typnamespace = 'public'::regnamespace
       AND e.enumlabel ILIKE '%familiar%'
     ORDER BY t.typname, e.enumsortorder
  LOOP
    newname := replace(replace(replace(r.enumlabel, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER TYPE %I RENAME VALUE %L TO %L', r.typname, r.enumlabel, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('enumvalue', r.typname, r.enumlabel, newname);
  END LOOP;

  -- 1i. RLS policies (none named familiar today; catalog-driven anyway)
  FOR r IN
    SELECT p.polname, p.polrelid::regclass::text AS rel
      FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
     WHERE c.relnamespace = 'public'::regnamespace
       AND p.polname ILIKE '%familiar%'
     ORDER BY p.polrelid, p.polname
  LOOP
    newname := replace(replace(replace(r.polname, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER POLICY %I ON %s RENAME TO %I', r.polname, r.rel, newname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('policy', r.rel, r.polname, newname);
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 2) CHECK constraints whose DEFINITION carries a familiar value: drop, update
--    the rows, re-add with the companion value (the re-add validates the data).
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  r      record;
  newdef text;
BEGIN
  FOR r IN
    SELECT c.conname, c.conrelid::regclass::text AS rel, pg_get_constraintdef(c.oid) AS def
      FROM pg_constraint c
     WHERE c.connamespace = 'public'::regnamespace
       AND c.contype = 'c'
       AND pg_get_constraintdef(c.oid) ILIKE '%familiar%'
     ORDER BY c.conrelid, c.conname
  LOOP
    newdef := replace(replace(replace(r.def, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('check', r.rel || '.' || r.conname, r.def, newdef);
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 2b) Lift FORCE RLS for the value rewrites below.
--
-- These rewrites run as chora_consumption_migrate, which OWNS these tables.
-- Under FORCE ROW LEVEL SECURITY the owner is NOT exempt, and with no tenant
-- GUC set every policy evaluates false, so an UPDATE matches ZERO rows and
-- silently no-ops. ALTER TABLE ... ADD CONSTRAINT, by contrast, validates
-- EVERY row, because constraint validation is not RLS-filtered.
--
-- THE WRITE IS FILTERED AND THE CHECK IS NOT. That asymmetry is the defect:
-- the rewrite quietly does nothing, then the constraint fails on the rows it
-- never touched. Proven live 2026-08-23: 22 concept_nodes rows sat at
-- familiar_suggested_accepted and the rewrite reported UPDATE 0.
--
-- A fresh build CANNOT reproduce this: with no rows, UPDATE 0 and a constraint
-- over an empty table both pass vacuously. Only populated data reaches it.
--
-- Lift FORCE for exactly the tables rewritten here, then restore the ORIGINAL
-- per-table state in 3z. Membership is discovered at runtime, so a table that
-- is not forced today is never touched and is never wrongly forced afterwards.
-- ----------------------------------------------------------------------------
CREATE TEMP TABLE _0110_forced ON COMMIT DROP AS
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
  FOR r IN SELECT rel FROM _0110_forced ORDER BY rel LOOP
    EXECUTE format('ALTER TABLE %s NO FORCE ROW LEVEL SECURITY', r.rel);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value)
      VALUES ('rls_force', r.rel, 'FORCE', 'NO FORCE');
  END LOOP;
END $$;

-- the values those CHECKs guard (concept provenance, ADR-212/214 concept graph)
UPDATE concept_nodes SET provenance = 'companion_suggested_accepted' WHERE provenance = 'familiar_suggested_accepted';
UPDATE concept_edges SET provenance = 'companion_suggested_accepted' WHERE provenance = 'familiar_suggested_accepted';

DO $$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT ident, new_value
      FROM companion_rename_0110_log
     WHERE kind = 'check'
     ORDER BY id
  LOOP
    EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s',
                   split_part(r.ident, '.', 1), split_part(r.ident, '.', 2), r.new_value);
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 3) Stored VALUES
-- ----------------------------------------------------------------------------
-- 3a. Skill catalogue keys (seedspec.LegacyNamesBefore0110: familiar_skill_* ->
--     companion_skill_*, fog_scout -> kg_explore, display name 'Fog Scout' ->
--     'Knowledge Explorer' per the owner ruling).
UPDATE companion_skill_catalog
   SET price_key = 'companion_skill_' || substr(price_key, length('familiar_skill_') + 1)
 WHERE price_key LIKE 'familiar\_skill\_%';

UPDATE companion_skill_catalog
   SET skill_key = 'kg_explore',
       price_key = 'companion_skill_kg_explore',
       -- learner-facing display name: owner ruling 2026-08-23 (ADR-254 copy row)
       name      = 'Knowledge Explorer'
 WHERE skill_key = 'fog_scout';

-- 3b. Species paths, ritual revisions (steps carry the Skill key as a value),
--     ritual run stamps: the "fog_scout" token inside JSONB.
UPDATE species_paths
   SET entries = replace(entries::text, '"fog_scout"', '"kg_explore"')::jsonb
 WHERE entries::text LIKE '%"fog_scout"%';

UPDATE companion_ritual_revisions
   SET steps = replace(steps::text, '"fog_scout"', '"kg_explore"')::jsonb
 WHERE steps::text LIKE '%"fog_scout"%';

UPDATE companion_ritual_runs
   SET decision_stamp = replace(decision_stamp::text, '"fog_scout"', '"kg_explore"')::jsonb
 WHERE decision_stamp::text LIKE '%"fog_scout"%';

-- 3c. Memory recall scope keys (companion.MemoryScopeKey = 'companion:' || id)
UPDATE companion_memory_recall
   SET scope_key = 'companion:' || substr(scope_key, length('familiar:') + 1)
 WHERE scope_key LIKE 'familiar:%';

-- 3d. Weakness upload review panel (weakness_upload.ReviewPanel JSON keys
--     familiar / familiar_id -> companion / companion_id; persisted JSONB)
UPDATE weakness_doc_uploads
   SET review_payload = replace(replace(review_payload::text, '"familiar_id":', '"companion_id":'), '"familiar":', '"companion":')::jsonb
 WHERE review_payload::text LIKE '%"familiar%';

-- 3e. Outbox rows not yet published: the dispatcher encodes by topic name and
--     the consumers move to the companion topics in the same window.
UPDATE outbox_events
   SET topic          = replace(topic, 'familiar', 'companion'),
       event_type     = replace(event_type, 'familiar', 'companion'),
       aggregate_type = replace(aggregate_type, 'familiar', 'companion')
 WHERE status IN ('pending', 'failed')
   AND (topic LIKE '%familiar%' OR event_type LIKE '%familiar%' OR aggregate_type LIKE '%familiar%');

-- 3f. Idempotency keys (the renamed handlers derive the same key with the
--     companion spelling; a redelivery across the cut must still be a no-op)
UPDATE idempotency_keys
   SET key = replace(key, 'familiar', 'companion')
 WHERE key LIKE '%familiar%';

-- ----------------------------------------------------------------------------
-- 3z) Restore FORCE RLS on exactly the tables lifted in 2b.
-- Same transaction, before COMMIT, so the window in which these tables are
-- owner-readable never outlives the migration.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  r record;
BEGIN
  FOR r IN SELECT rel FROM _0110_forced ORDER BY rel LOOP
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', r.rel);
    INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value)
      VALUES ('rls_force', r.rel, 'NO FORCE', 'FORCE');
  END LOOP;
END $$;

-- Fail-loud assert: a table lifted in 2b and not restored here would be left
-- permanently owner-readable, which is a tenant-isolation regression. Refuse
-- to commit rather than ship it.
DO $$
DECLARE
  n int;
BEGIN
  SELECT count(*) INTO n
    FROM _0110_forced f
    JOIN pg_class c ON c.oid = f.rel::regclass
   WHERE NOT c.relforcerowsecurity;
  IF n > 0 THEN
    RAISE EXCEPTION '0110: % table(s) left without FORCE ROW LEVEL SECURITY', n;
  END IF;
END $$;

-- 3g. Object comments (tables, indexes, views, sequences, columns)
DO $$
DECLARE
  r       record;
  newdesc text;
BEGIN
  FOR r IN
    SELECT c.relname, c.relkind::text AS relkind, d.objsubid, d.description, a.attname
      FROM pg_description d
      JOIN pg_class c ON c.oid = d.objoid AND d.classoid = 'pg_class'::regclass
      LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = d.objsubid AND d.objsubid > 0
     WHERE c.relnamespace = 'public'::regnamespace
       AND d.description ILIKE '%familiar%'
     ORDER BY c.relname, d.objsubid
  LOOP
    newdesc := replace(replace(replace(r.description, 'familiar', 'companion'), 'Familiar', 'Companion'), 'FAMILIAR', 'COMPANION');
    IF r.objsubid > 0 THEN
      EXECUTE format('COMMENT ON COLUMN %I.%I IS %L', r.relname, r.attname, newdesc);
      INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('comment_column', r.relname || '.' || r.attname, r.description, newdesc);
    ELSE
      CASE r.relkind
        WHEN 'i' THEN EXECUTE format('COMMENT ON INDEX %I IS %L', r.relname, newdesc);
        WHEN 'I' THEN EXECUTE format('COMMENT ON INDEX %I IS %L', r.relname, newdesc);
        WHEN 'v' THEN EXECUTE format('COMMENT ON VIEW %I IS %L', r.relname, newdesc);
        WHEN 'm' THEN EXECUTE format('COMMENT ON MATERIALIZED VIEW %I IS %L', r.relname, newdesc);
        WHEN 'S' THEN EXECUTE format('COMMENT ON SEQUENCE %I IS %L', r.relname, newdesc);
        ELSE          EXECUTE format('COMMENT ON TABLE %I IS %L', r.relname, newdesc);
      END CASE;
      INSERT INTO companion_rename_0110_log(kind, ident, old_value, new_value) VALUES ('comment_' || r.relkind, r.relname, r.description, newdesc);
    END IF;
  END LOOP;
END $$;

-- ----------------------------------------------------------------------------
-- 4) Assertions: nothing familiar-named or familiar-defined may remain.
-- ----------------------------------------------------------------------------
DO $$
DECLARE
  leftovers text;
BEGIN
  SELECT string_agg(x, ', ') INTO leftovers FROM (
    SELECT 'relation ' || relname AS x FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relname ILIKE '%familiar%'
    UNION ALL
    SELECT 'column ' || c.relname || '.' || a.attname FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
     WHERE c.relnamespace = 'public'::regnamespace AND a.attnum > 0 AND NOT a.attisdropped AND a.attname ILIKE '%familiar%'
    UNION ALL
    SELECT 'constraint ' || conname FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND (conname ILIKE '%familiar%' OR pg_get_constraintdef(oid) ILIKE '%familiar%')
    UNION ALL
    SELECT 'type ' || typname FROM pg_type WHERE typnamespace = 'public'::regnamespace AND typname ILIKE '%familiar%'
    UNION ALL
    SELECT 'enum value ' || t.typname || '.' || e.enumlabel FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid
     WHERE t.typnamespace = 'public'::regnamespace AND e.enumlabel ILIKE '%familiar%'
    UNION ALL
    SELECT 'function ' || proname FROM pg_proc WHERE pronamespace = 'public'::regnamespace AND (proname ILIKE '%familiar%' OR prosrc ILIKE '%familiar%')
    UNION ALL
    SELECT 'trigger ' || tgname FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
     WHERE NOT t.tgisinternal AND c.relnamespace = 'public'::regnamespace AND t.tgname ILIKE '%familiar%'
    UNION ALL
    SELECT 'policy ' || polname FROM pg_policy
     WHERE polname ILIKE '%familiar%'
        OR COALESCE(pg_get_expr(polqual, polrelid), '') ILIKE '%familiar%'
        OR COALESCE(pg_get_expr(polwithcheck, polrelid), '') ILIKE '%familiar%'
    UNION ALL
    SELECT 'view ' || c.relname FROM pg_class c WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('v', 'm') AND pg_get_viewdef(c.oid) ILIKE '%familiar%'
    UNION ALL
    SELECT 'index ' || i.indexrelid::regclass::text FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid
     WHERE c.relnamespace = 'public'::regnamespace AND pg_get_indexdef(i.indexrelid) ILIKE '%familiar%'
    UNION ALL
    SELECT 'catalogue row ' || skill_key FROM companion_skill_catalog WHERE skill_key = 'fog_scout' OR price_key ILIKE '%familiar%'
    UNION ALL
    SELECT 'species path ' || species FROM species_paths WHERE entries::text LIKE '%"fog_scout"%'
    UNION ALL
    SELECT 'memory scope ' || scope_key FROM companion_memory_recall WHERE scope_key LIKE 'familiar:%'
    UNION ALL
    SELECT 'concept_nodes provenance ' || provenance FROM concept_nodes WHERE provenance ILIKE '%familiar%'
    UNION ALL
    SELECT 'concept_edges provenance ' || provenance FROM concept_edges WHERE provenance ILIKE '%familiar%'
  ) t;
  IF leftovers IS NOT NULL THEN
    RAISE EXCEPTION '0110_companion_rename: familiar leftovers after rename: %', leftovers;
  END IF;
END $$;

COMMIT;

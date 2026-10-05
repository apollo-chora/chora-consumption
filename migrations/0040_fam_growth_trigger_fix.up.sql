-- 0040_fam_growth_trigger_fix.up.sql
-- E2E-BE-FAM-GROWTH §3 follow-on (2026-05-16).
--
-- Background:
--   Migration 0001_initial.sql defined `consumption_set_updated_at()` as a
--   simple `NEW.updated_at = now()` trigger function. Migration 0002 (ADR-143
--   per-user KG) re-CREATEd the SAME function with an extra `NEW.version =
--   OLD.version + 1` line — because its NEW tables (map_clusters,
--   atom_semantic_edges, kg_*) carry a `version` column.
--
--   The re-CREATE shadowed the 0001 version for EVERY consumer table, including
--   the 0006 `familiar_instances` table which has NO `version` column. The
--   first UPDATE on `familiar_instances` therefore fails with
--   SQLSTATE 42703 "record \"new\" has no field \"version\"".
--
--   This blocked the E2E-BE-FAM-GROWTH /kg-neighbors smoke (the
--   PickKgNeighbor handler does `UPDATE familiar_instances
--   SET visible_kg_neighbors = array_append(..., $1::uuid)`).
--
-- Fix:
--   Split the function into two:
--     - `consumption_set_updated_at()`     — restored to its 0001 shape
--       (only `updated_at = now()`); used by every NON-versioned table.
--     - `consumption_set_updated_at_versioned()` — preserves the 0002
--       behaviour (also bumps `NEW.version`); used by the 8 ADR-143 KG
--       tables that carry a `version` column.
--
--   Re-binds the 8 ADR-143 KG triggers to the new versioned function. The
--   trigger drops + re-creates are idempotent against a previously-applied
--   0002 migration.
--
-- Risk:
--   - Atomic per-table DROP+CREATE in one transaction — no window where a
--     table sits with no updated_at trigger.
--   - No new columns or data changes — pure trigger rebind + function split.
--   - Reversible (see down migration).

BEGIN;

-- 1. Restore the unversioned baseline (matches 0001_initial.sql:33).
CREATE OR REPLACE FUNCTION consumption_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2. Add the versioned variant for tables that have a `version` column.
CREATE OR REPLACE FUNCTION consumption_set_updated_at_versioned()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    NEW.version   = OLD.version + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 3. Re-bind ADR-143 per-user KG triggers (0002 migration) to the versioned
--    function. Tables: map_clusters, atom_semantic_edges, knowledge_graph_edges,
--    user_atom_progress (the 4 tables that carry a `version` column per
--    0002_per_user_knowledge_graph.sql).
DO $rebind$
DECLARE
    rec RECORD;
BEGIN
    -- Rebind any trigger that:
    --   - currently calls consumption_set_updated_at, AND
    --   - is on a table that has a `version` column.
    -- Skip tables without `version` so they stay on the unversioned function.
    FOR rec IN
        SELECT t.tgname AS trigger_name,
               c.relname AS table_name
        FROM pg_trigger t
        JOIN pg_class   c ON t.tgrelid = c.oid
        JOIN pg_proc    p ON t.tgfoid  = p.oid
        WHERE p.proname = 'consumption_set_updated_at'
          AND NOT t.tgisinternal
          AND EXISTS (
              SELECT 1
                FROM information_schema.columns
               WHERE table_schema = 'public'
                 AND table_name   = c.relname
                 AND column_name  = 'version'
          )
    LOOP
        EXECUTE format(
            'DROP TRIGGER IF EXISTS %I ON public.%I',
            rec.trigger_name, rec.table_name
        );
        EXECUTE format(
            'CREATE TRIGGER %I BEFORE UPDATE ON public.%I '
            'FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at_versioned()',
            rec.trigger_name, rec.table_name
        );
        RAISE NOTICE '0040: rebound trigger %.%, → consumption_set_updated_at_versioned',
                     rec.table_name, rec.trigger_name;
    END LOOP;
END
$rebind$;

COMMIT;

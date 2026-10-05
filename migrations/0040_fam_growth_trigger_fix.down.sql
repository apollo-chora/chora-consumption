-- 0040_fam_growth_trigger_fix.down.sql
--
-- Restores the broken 0002 state for full reversibility:
--   - Re-bind ADR-143 triggers to consumption_set_updated_at.
--   - Re-CREATE consumption_set_updated_at with the buggy `NEW.version` line.
--   - Drop consumption_set_updated_at_versioned.
--
-- Down-migration is provided for reversibility, not for production rollback —
-- the down path re-introduces the SQLSTATE 42703 regression on every
-- familiar_instances UPDATE. Use with care.

BEGIN;

DO $rebind_back$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT t.tgname AS trigger_name,
               c.relname AS table_name
        FROM pg_trigger t
        JOIN pg_class   c ON t.tgrelid = c.oid
        JOIN pg_proc    p ON t.tgfoid  = p.oid
        WHERE p.proname = 'consumption_set_updated_at_versioned'
          AND NOT t.tgisinternal
    LOOP
        EXECUTE format(
            'DROP TRIGGER IF EXISTS %I ON public.%I',
            rec.trigger_name, rec.table_name
        );
        EXECUTE format(
            'CREATE TRIGGER %I BEFORE UPDATE ON public.%I '
            'FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at()',
            rec.trigger_name, rec.table_name
        );
    END LOOP;
END
$rebind_back$;

-- Re-CREATE the buggy versioned trigger (matches 0002:27 pre-0040 behaviour).
CREATE OR REPLACE FUNCTION consumption_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    NEW.version   = OLD.version + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP FUNCTION IF EXISTS consumption_set_updated_at_versioned();

COMMIT;

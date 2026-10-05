-- =============================================================================
-- chora-consumption : 0117_companion_skill_ports.up.sql
-- Domain: Content Consumption (5 core) / Database: chora_consumption
-- Story : ADR-257 D2 (ritual step chaining, typed and reconciled hand-off),
--         tracker row B3 / N1. Adds the RESERVED inter-step port columns to
--         the platform capability catalogue.
--
-- SCHEMA ONLY, and deliberately so. Both columns default to 'none', nothing
-- reads them, and no row is authored here: the per-Skill values are DERIVED
-- from ToolHandlerRefs + OutputSink (which are already drift-gated through
-- seedspec) and land in their own data migration when chaining ships behind
-- the ADR-257 section 5 tool-allowlist enforcement gate. Authoring values now
-- would turn a reservation into a claim about behaviour that does not exist.
--
-- The 'none' default is what makes this safe to apply ahead of any reader:
-- every existing row reads as "no ports", so no published revision becomes
-- unpublishable and no code path changes behaviour on apply.
--
-- Vocabulary note: the value list is a SUPERSET of the five SkillParamKind
-- values (internal/domain/companion/skill_params.go), so the composer has ONE
-- type system rather than two. companion.IsKnownPort is the Go side, and
-- companion_skill_ports_migration_test.go pins the two against each other in
-- BOTH directions: a Go constant absent from this CHECK would be refused at
-- INSERT time in production, long after the Go change looked complete.
--
-- Shape notes (mirrors the 0061 catalogue-v2 idiom):
--   - ADD COLUMN IF NOT EXISTS + the DO $$ ... EXCEPTION WHEN duplicate_object
--     constraint wrapper, so the migration is idempotent and re-runnable.
--   - No RLS change: companion_skill_catalog is PLATFORM-scoped reference data
--     with no tenant axis, so it carries no tenant_isolation policy to widen.
--   - GRANTs delegated to 9999_grant_app_roles.sql (GRANT ... ON ALL TABLES
--     covers a new column on an existing table automatically).
--
-- HARD INVARIANT: idempotent + revertable. Do NOT apply manually; migrations
-- auto-apply at deploy.
-- =============================================================================
BEGIN;

ALTER TABLE companion_skill_catalog
    ADD COLUMN IF NOT EXISTS consumes TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS produces TEXT NOT NULL DEFAULT 'none';

COMMENT ON COLUMN companion_skill_catalog.consumes IS
    'ADR-257 D2 RESERVED: the port type this Skill can be fed by an earlier ritual step. Default none = no input, which is exactly v1 behaviour. Values derived from tool_handler_ref + output_sink in a later data migration.';

COMMENT ON COLUMN companion_skill_catalog.produces IS
    'ADR-257 D2 RESERVED: the port type this Skill hands to a later ritual step. Default none. Reference types reconcile against a deterministic pool at every boundary and may cross freely; text reconciles against nothing and crosses once, marked unverified (owner ruling R23).';

DO $$ BEGIN
    ALTER TABLE companion_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_consumes
        CHECK (consumes IN ('none','text','concept_ref','growth_edge_ref',
                            'atom_ref','question_ref','source_ref'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE companion_skill_catalog
        ADD CONSTRAINT chk_skill_catalog_produces
        CHECK (produces IN ('none','text','concept_ref','growth_edge_ref',
                            'atom_ref','question_ref','source_ref'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

COMMIT;

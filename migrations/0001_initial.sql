-- =============================================================================
-- chora-consumption : 0001_initial.sql
--
-- Domain        : Content Consumption (5 core)
-- Database      : chora_consumption
-- Author        : agent-a5e52e89b73ede1d2 (db-migrations-11-services)
-- Date          : 2026-05-08
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1)
--
-- Aggregates owned by this database:
--   - LearningPath (owner_gcid + atom_ids[])
--   - PathEnrollment (per-learner progress)
--   - AtomicSession (state-machine: started / in_progress / completed / abandoned)
--   - Familiar (RPG companion, archetype + stats + traits)
--   - SM-2 spaced-repetition state per (gcid, atom_id)
--   - Learner embeddings (pgvector — retention/curiosity-similarity)
--
-- Atom IDs reference rows in chora_creation; cross-DB JOINs FORBIDDEN.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- Extensions
-- -----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "vector";  -- pgvector 0.8+ (CLAUDE.md §1)

-- -----------------------------------------------------------------------------
-- Shared trigger: bump updated_at
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION consumption_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- ENUMs (mirror Go domain enums)
-- -----------------------------------------------------------------------------
CREATE TYPE atomic_session_status AS ENUM ('started', 'in_progress', 'completed', 'abandoned');
CREATE TYPE familiar_archetype    AS ENUM ('owl', 'fox', 'dragon', 'phoenix', 'wolf');
CREATE TYPE sm2_grade             AS ENUM ('i_remember', 'not_sure', 'forgot');

-- -----------------------------------------------------------------------------
-- learning_paths — Straight-Up linear cert mode aggregate
-- -----------------------------------------------------------------------------
CREATE TABLE learning_paths (
    path_id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID        NOT NULL,
    owner_gcid        UUID        NOT NULL,
    title             VARCHAR(256) NOT NULL,
    description       TEXT         NOT NULL DEFAULT '',
    atom_ids          UUID[]       NOT NULL DEFAULT '{}',  -- ordered atom UUIDs (cross-DB ref)
    completed         BOOLEAN      NOT NULL DEFAULT FALSE,
    completed_at      TIMESTAMPTZ  NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ  NULL,
    CHECK (cardinality(atom_ids) >= 1)
);

CREATE INDEX idx_learning_paths_tenant ON learning_paths (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_learning_paths_owner  ON learning_paths (owner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_learning_paths_atoms_gin ON learning_paths USING GIN (atom_ids);

CREATE TRIGGER trg_learning_paths_updated_at
    BEFORE UPDATE ON learning_paths
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE learning_paths ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON learning_paths
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- path_enrollments — per-learner progress projection
-- -----------------------------------------------------------------------------
CREATE TABLE path_enrollments (
    enrollment_id        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID        NOT NULL,
    path_id              UUID        NOT NULL REFERENCES learning_paths(path_id) ON DELETE RESTRICT,
    learner_gcid         UUID        NOT NULL,
    completed_atom_ids   UUID[]      NOT NULL DEFAULT '{}',
    enrolled_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ NULL,
    UNIQUE (path_id, learner_gcid)
);

CREATE INDEX idx_path_enrollments_tenant     ON path_enrollments (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_path_enrollments_learner    ON path_enrollments (learner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_path_enrollments_path       ON path_enrollments (path_id) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_path_enrollments_updated_at
    BEFORE UPDATE ON path_enrollments
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE path_enrollments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON path_enrollments
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- atomic_sessions — explicit state-machine (started / in_progress / completed / abandoned)
-- -----------------------------------------------------------------------------
CREATE TABLE atomic_sessions (
    session_id        UUID                  PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID                  NOT NULL,
    learner_gcid      UUID                  NOT NULL,
    atom_id           UUID                  NOT NULL,         -- cross-DB ref to chora_creation
    status            atomic_session_status NOT NULL DEFAULT 'started',
    hints_used        SMALLINT              NOT NULL DEFAULT 0 CHECK (hints_used BETWEEN 0 AND 3),
    answer_count      INTEGER               NOT NULL DEFAULT 0,
    last_answer       TEXT                  NULL,
    started_at        TIMESTAMPTZ           NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ           NULL,
    abandoned_at      TIMESTAMPTZ           NULL,
    updated_at        TIMESTAMPTZ           NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ           NULL
);

CREATE INDEX idx_atomic_sessions_tenant   ON atomic_sessions (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_atomic_sessions_learner  ON atomic_sessions (learner_gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_atomic_sessions_atom     ON atomic_sessions (atom_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_atomic_sessions_status   ON atomic_sessions (status) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_atomic_sessions_updated_at
    BEFORE UPDATE ON atomic_sessions
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE atomic_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON atomic_sessions
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- familiars — RPG companion entity (1:1 with owner_gcid per tenant)
--
-- DOMAIN ENTITY ONLY — the AI agent powering the Familiar lives in AI Kernel
-- as a separate adapter concern (feedback_familiar_vs_agent memory).
-- -----------------------------------------------------------------------------
CREATE TABLE familiars (
    familiar_id       UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         UUID                NOT NULL,
    owner_gcid        UUID                NOT NULL,
    name              VARCHAR(64)         NOT NULL,
    archetype         familiar_archetype  NULL,           -- empty pre-summon (auto-bond legacy)
    level             INTEGER             NOT NULL DEFAULT 1 CHECK (level >= 1),
    xp                INTEGER             NOT NULL DEFAULT 0 CHECK (xp >= 0),
    traits            TEXT[]              NOT NULL DEFAULT '{}',
    unlocked_traits   TEXT[]              NOT NULL DEFAULT '{}',
    stats             JSONB               NOT NULL DEFAULT '{}'::jsonb,    -- 5-dim stat vector
    created_at        TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ         NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ         NULL,
    UNIQUE (tenant_id, owner_gcid)
);

CREATE INDEX idx_familiars_tenant ON familiars (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_familiars_owner  ON familiars (owner_gcid) WHERE deleted_at IS NULL;

CREATE TRIGGER trg_familiars_updated_at
    BEFORE UPDATE ON familiars
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE familiars ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON familiars
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- sm2_feedback — SM-2 spaced-repetition state per (gcid, atom_id)
--
-- Implements the Ebbinghaus Forgetting Curve primitive (CLAUDE.md §1).
-- -----------------------------------------------------------------------------
CREATE TABLE sm2_feedback (
    sm2_id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID         NOT NULL,
    gcid                UUID         NOT NULL,
    atom_id             UUID         NOT NULL,                        -- cross-DB ref
    repetitions         INTEGER      NOT NULL DEFAULT 0 CHECK (repetitions >= 0),
    ease_factor         REAL         NOT NULL DEFAULT 2.5 CHECK (ease_factor >= 1.3),
    interval_days       INTEGER      NOT NULL DEFAULT 0 CHECK (interval_days >= 0),
    last_grade          sm2_grade    NULL,
    last_reviewed_at    TIMESTAMPTZ  NULL,
    next_review_date    TIMESTAMPTZ  NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, gcid, atom_id)
);

CREATE INDEX idx_sm2_feedback_owner    ON sm2_feedback (tenant_id, gcid);
CREATE INDEX idx_sm2_feedback_due      ON sm2_feedback (gcid, next_review_date)
    WHERE next_review_date IS NOT NULL;

CREATE TRIGGER trg_sm2_feedback_updated_at
    BEFORE UPDATE ON sm2_feedback
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE sm2_feedback ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sm2_feedback
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- learner_embeddings — pgvector retention/curiosity-similarity vectors
-- -----------------------------------------------------------------------------
CREATE TABLE learner_embeddings (
    gcid              UUID         PRIMARY KEY,
    tenant_id         UUID         NOT NULL,
    embedding         vector(768)  NOT NULL,
    model_id          VARCHAR(64)  NOT NULL,
    embedding_version INTEGER      NOT NULL DEFAULT 1,
    refreshed_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_learner_embeddings_cosine
    ON learner_embeddings USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
CREATE INDEX idx_learner_embeddings_tenant ON learner_embeddings (tenant_id);

CREATE TRIGGER trg_learner_embeddings_updated_at
    BEFORE UPDATE ON learner_embeddings
    FOR EACH ROW EXECUTE FUNCTION consumption_set_updated_at();

ALTER TABLE learner_embeddings ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON learner_embeddings
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;

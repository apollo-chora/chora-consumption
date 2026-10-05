-- =============================================================================
-- chora-consumption : 0020_survey.sql
--
-- Consolidates `chora-survey` legacy migrations into chora-consumption per
-- M12.2 Batch 3.a. Surveys live in the Content Consumption domain because
-- surveys are typically delivered post-AtomicSession or post-LearningPath.
--
-- Source: chora-survey/migrations/001-005 (up only)
-- Domain: Content Consumption (5 core)
-- Database: chora_consumption
-- RLS: tenant-scoped (preserved verbatim).
-- =============================================================================

BEGIN;

-- ==========================================================================
-- Migration: 001_create_extensions_and_enums.up.sql (survey consolidation)
-- ==========================================================================
-- 001_create_extensions_and_enums.up.sql
-- Extensions and ENUM types for chora-survey.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Survey template lifecycle status.
-- draft: initial state, can be modified freely
-- published: survey is available for learner responses
-- archived: survey is no longer accepting responses
CREATE TYPE survey_status AS ENUM (
    'draft',
    'published',
    'archived'
);

-- Survey question type.
-- rating: numeric star/rating (1-5 or 1-10)
-- text: free-form text response
-- multiple_choice: select from predefined options
-- scale: Likert scale (e.g., 1-7 agree/disagree)
CREATE TYPE question_type AS ENUM (
    'rating',
    'text',
    'multiple_choice',
    'scale'
);

-- Shared trigger function: auto-update updated_at on row modification.
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ==========================================================================
-- Migration: 002_create_survey_templates.up.sql (survey consolidation)
-- ==========================================================================
-- 002_create_survey_templates.up.sql
-- SurveyTemplate: aggregate root for survey administration.
-- Tenant-scoped with RLS. Soft-delete via deleted_at.

CREATE TABLE survey_templates (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    title            VARCHAR(255) NOT NULL,
    description      TEXT,
    status           survey_status NOT NULL DEFAULT 'draft',
    linked_session_id UUID,               -- Cross-context ref to TrainingSession (no FK)
    question_count   INTEGER NOT NULL DEFAULT 0,
    created_by_gcid  UUID NOT NULL,       -- Cross-context ref to GCID (no FK)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at       TIMESTAMPTZ          -- Soft delete (DDD rule #5)
);

-- Indexes
CREATE INDEX idx_survey_templates_tenant_id ON survey_templates(tenant_id);
CREATE INDEX idx_survey_templates_status ON survey_templates(tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_survey_templates_linked_session ON survey_templates(linked_session_id) WHERE deleted_at IS NULL;

-- Auto-update updated_at
CREATE TRIGGER survey_templates_updated_at
    BEFORE UPDATE ON survey_templates
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at();

-- Row-Level Security
ALTER TABLE survey_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE survey_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON survey_templates
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 003_create_survey_questions.up.sql (survey consolidation)
-- ==========================================================================
-- 003_create_survey_questions.up.sql
-- SurveyQuestion: child entity of SurveyTemplate, accessed only through
-- the aggregate root. Tenant-scoped via survey_templates FK.
-- Soft-delete via deleted_at (cascade within aggregate).

CREATE TABLE survey_questions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    survey_id     UUID NOT NULL REFERENCES survey_templates(id) ON DELETE CASCADE,
    question_text TEXT NOT NULL,
    question_type question_type NOT NULL,
    options       JSONB DEFAULT '{}',
    order_index   INTEGER NOT NULL DEFAULT 0,
    required      BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ          -- Soft delete (cascade within aggregate)
);

-- Indexes
CREATE INDEX idx_survey_questions_survey_id ON survey_questions(survey_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_survey_questions_order ON survey_questions(survey_id, order_index) WHERE deleted_at IS NULL;

-- Row-Level Security (tenant isolation via join to survey_templates)
ALTER TABLE survey_questions ENABLE ROW LEVEL SECURITY;
ALTER TABLE survey_questions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON survey_questions
    FOR ALL USING (
        EXISTS (
            SELECT 1 FROM survey_templates st
            WHERE st.id = survey_questions.survey_id
            AND st.tenant_id = current_setting('app.current_tenant_id', true)::uuid
        )
    );

-- ==========================================================================
-- Migration: 004_create_survey_responses.up.sql (survey consolidation)
-- ==========================================================================
-- 004_create_survey_responses.up.sql
-- SurveyResponse: append-only record of a learner's response to a survey.
-- Tenant-scoped with RLS. No updates or deletes (append-only).

CREATE TABLE survey_responses (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    survey_id    UUID NOT NULL REFERENCES survey_templates(id),
    gcid         UUID NOT NULL,           -- Cross-context ref to GCID (no FK)
    answers      JSONB NOT NULL DEFAULT '{}',
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_survey_responses_tenant_id ON survey_responses(tenant_id);
CREATE INDEX idx_survey_responses_survey_id ON survey_responses(survey_id);
CREATE INDEX idx_survey_responses_gcid ON survey_responses(survey_id, gcid);

-- Unique: one response per GCID per survey
CREATE UNIQUE INDEX idx_survey_responses_unique_respondent
    ON survey_responses(survey_id, gcid);

-- Row-Level Security
ALTER TABLE survey_responses ENABLE ROW LEVEL SECURITY;
ALTER TABLE survey_responses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON survey_responses
    FOR ALL USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ==========================================================================
-- Migration: 005_processed_events.up.sql (survey consolidation)
-- ==========================================================================
-- 005_processed_events.up.sql
-- Idempotency store for event processing.

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     UUID PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- TTL index for cleanup (events older than 7 days can be purged)
CREATE INDEX idx_processed_events_processed_at ON processed_events(processed_at);


COMMIT;

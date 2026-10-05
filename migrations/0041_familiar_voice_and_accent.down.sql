-- =============================================================================
-- chora-consumption : 0041_familiar_voice_and_accent.down.sql
-- =============================================================================

BEGIN;

DROP TRIGGER IF EXISTS trg_familiar_accent_unlock_no_delete ON familiar_accent_unlock;
DROP TRIGGER IF EXISTS trg_familiar_accent_unlock_no_update ON familiar_accent_unlock;
DROP FUNCTION IF EXISTS reject_familiar_accent_unlock_mutation();
DROP TABLE IF EXISTS familiar_accent_unlock;

DROP TRIGGER IF EXISTS trg_familiar_voice_config_updated_at ON familiar_voice_config;
DROP TABLE IF EXISTS familiar_voice_config;

COMMIT;

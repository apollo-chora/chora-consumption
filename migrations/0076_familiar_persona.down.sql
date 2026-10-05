-- 0076_familiar_persona.down.sql — reverse the Persona sheet columns.
ALTER TABLE familiar_instances
  DROP COLUMN IF EXISTS persona_version,
  DROP COLUMN IF EXISTS guidance_note;

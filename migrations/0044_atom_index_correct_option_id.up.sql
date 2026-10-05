-- CHO-1627: identity-based MCQ grading — grade by stable option_id, not positional index.
ALTER TABLE atom_index ADD COLUMN IF NOT EXISTS correct_option_id TEXT NOT NULL DEFAULT '';

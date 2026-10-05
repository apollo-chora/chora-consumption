-- 0091 down: restore the atom_index.correct_index column shape (CHO-2142).
-- Shape-only — the column was never populated (no writer ever named it), so
-- there are no values to restore and the restored column is uniformly NULL.
-- MCQ grading stays on correct_option_id (0044 / CHO-1627) either way.
ALTER TABLE atom_index ADD COLUMN IF NOT EXISTS correct_index INTEGER NULL;

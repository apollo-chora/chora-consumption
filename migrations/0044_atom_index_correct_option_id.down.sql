-- CHO-1627 rollback: drop the identity-based MCQ answer-key column.
ALTER TABLE atom_index DROP COLUMN IF EXISTS correct_option_id;

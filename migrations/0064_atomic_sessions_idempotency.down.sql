ALTER TABLE atomic_sessions
    DROP COLUMN IF EXISTS last_answer_id,
    DROP COLUMN IF EXISTS last_correct,
    DROP COLUMN IF EXISTS last_result_status;

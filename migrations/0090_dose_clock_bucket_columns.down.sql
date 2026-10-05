-- 0090 down — restore the DATE day-bucket columns (drops sub-day
-- resolution; only safe when the doseclock runs at product per-day speed).
ALTER TABLE campaign_question_sets
    ALTER COLUMN requested_on TYPE DATE
    USING ((requested_on AT TIME ZONE 'UTC')::date);

ALTER TABLE campaign_node_progress
    ALTER COLUMN last_advance_date TYPE DATE
    USING ((last_advance_date AT TIME ZONE 'UTC')::date);

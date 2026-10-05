// dose_clock_migration_test.go — CHO-2087: content gate for migration 0090
// (dose-clock bucket columns). The env-tunable doseclock needs sub-day
// resolution on the two day-bucket columns; DATE silently collapses every
// bucket of one calendar day (breaking the D13 budget count + D7 pacing
// stamp at accelerated test speed). Mirrors the 0077/0079 gate idiom.
package pg

import "testing"

const (
	doseClockMigrationUp   = "0090_dose_clock_bucket_columns.up.sql"
	doseClockMigrationDown = "0090_dose_clock_bucket_columns.down.sql"
)

func TestMigration0090_BucketColumnsToTimestamptz(t *testing.T) {
	sql := readMigration(t, doseClockMigrationUp)
	assertContainsAll(t, doseClockMigrationUp, sql, []string{
		"ALTER TABLE campaign_question_sets",
		"ALTER COLUMN requested_on TYPE timestamptz",
		"USING (requested_on::timestamp AT TIME ZONE 'UTC')",
		"ALTER TABLE campaign_node_progress",
		"ALTER COLUMN last_advance_date TYPE timestamptz",
		"USING (last_advance_date::timestamp AT TIME ZONE 'UTC')",
	})
}

func TestMigration0090_DownRestoresDate(t *testing.T) {
	sql := readMigration(t, doseClockMigrationDown)
	assertContainsAll(t, doseClockMigrationDown, sql, []string{
		"ALTER COLUMN requested_on TYPE DATE",
		"ALTER COLUMN last_advance_date TYPE DATE",
	})
}

//go:build integration

package pg

// student_transcript_prepare_smoke_integration_test.go: PREPARE every
// StudentTranscript SQL const against a REAL PostgreSQL so server-side parse
// analysis runs (parameter type deduction, column existence, cast validity).
//
// Why this file exists. The transcript consts were in NO prepare-smoke map:
// the two checks that name them assert only that the template is non-empty and
// that it mentions delivery_type. So the B6 mark-seen write and the unseen
// count both shipped, or would have shipped, with nothing but Go compilation
// behind their SQL, and a 42703 on a column that does not exist would have
// reached production green. That is the exact CHO-2012 class this harness
// exists for.
//
// Requires migrations 0079_student_transcript and 0118_student_transcript_seen_at
// applied on the target DB.
//
// Run:
//
//	export CHORA_TEST_DSN=postgres://...
//	go test -tags integration -run TestStudentTranscriptPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// studentTranscriptPrepareSmokeStatements enumerates every SQL const the
// transcript adapters execute. ADD NEW CONSTS HERE when an adapter grows one.
var studentTranscriptPrepareSmokeStatements = map[string]string{
	// student_transcript.go
	"upsertTranscriptEntrySQL":         upsertTranscriptEntrySQL,
	"listTranscriptByGCIDSQL":          listTranscriptByGCIDSQL,
	"listTranscriptByAssessmentIDsSQL": listTranscriptByAssessmentIDsSQL,
	// student_transcript_seen.go (B6 item 2 + the D2 unseen roll-up)
	"markTranscriptEntrySeenSQL":      markTranscriptEntrySeenSQL,
	"countUnseenTranscriptEntriesSQL": countUnseenTranscriptEntriesSQL,
}

func TestStudentTranscriptPrepareSmoke_Parse(t *testing.T) {
	dsn := os.Getenv("CHORA_TEST_DSN")
	if dsn == "" {
		t.Skip("CHORA_TEST_DSN not set; skipping PREPARE smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for name, sql := range studentTranscriptPrepareSmokeStatements {
		stmt := fmt.Sprintf("smoke_transcript_%s", name)
		if _, err := conn.Prepare(ctx, stmt, sql); err != nil {
			t.Errorf("PREPARE %s failed (real-PG parse/plan defect): %v", name, err)
		}
	}
}

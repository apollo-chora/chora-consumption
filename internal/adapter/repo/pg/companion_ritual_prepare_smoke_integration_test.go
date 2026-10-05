//go:build integration

package pg

// companion_ritual_prepare_smoke_integration_test.go — PREPARE every Ritual SQL
// const (companion_ritual_repo.go + companion_ritual_run_repo.go) against a REAL
// PostgreSQL so server-side parse analysis runs (parameter type deduction,
// column existence, cast validity, ON CONFLICT arbiter inference, the
// $3::uuid-IS-NULL guard). Mirrors dose_kg_pref_prepare_smoke's contract
// (CHO-2012 lesson: unit stubs let a real 42703/42P01 ship green).
//
// Self-contained (own statements map + own test name) — CHO-2016 G3 is
// new-files-only, so the shared prepareSmokeStatements map is NOT edited here;
// wire these consts into it when the repos are constructed in cmd/server (G5).
//
// Requires migration 0071_familiar_rituals applied on the target DB.
//
// Run:
//
//	export CHORA_TEST_DSN=postgres://...    # DB with migration 0071 applied
//	go test -tags integration -run TestCompanionRitualPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// companionRitualPrepareSmokeStatements enumerates every SQL const the two
// Ritual adapters execute. ADD NEW CONSTS HERE when an adapter grows a statement.
var companionRitualPrepareSmokeStatements = map[string]string{
	// companion_ritual_repo.go
	"insertRitualSQL":           insertRitualSQL,
	"getRitualSQL":              getRitualSQL,
	"listRitualsByCompanionSQL": listRitualsByCompanionSQL,
	"listRitualRevisionsSQL":    listRitualRevisionsSQL,
	"insertRitualRevisionSQL":   insertRitualRevisionSQL,
	"bumpRitualRevisionSQL":     bumpRitualRevisionSQL,
	"setRitualEnabledSQL":       setRitualEnabledSQL,
	"countEnabledRitualsSQL":    countEnabledRitualsSQL,
	"softDeleteRitualSQL":       softDeleteRitualSQL,
	// companion_ritual_run_repo.go
	"createRitualRunSQL":      createRitualRunSQL,
	"updateRitualRunSQL":      updateRitualRunSQL,
	"sweepStaleRitualRunsSQL": sweepStaleRitualRunsSQL,
	"listRunsByRitualSQL":     listRunsByRitualSQL,
	"getRitualRunSQL":         getRitualRunSQL,
}

// TestCompanionRitualPrepareSmoke_Parse PREPAREs every statement on one
// connection. Any 42P08 / 42703 / 42P01 / 42P10-class defect fails loud with
// the const name.
func TestCompanionRitualPrepareSmoke_Parse(t *testing.T) {
	dsn := os.Getenv("CHORA_TEST_DSN")
	if dsn == "" {
		t.Skip("set CHORA_TEST_DSN to run prepare-smoke integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	i := 0
	for name, sql := range companionRitualPrepareSmokeStatements {
		i++
		stmtName := fmt.Sprintf("ritual_smoke_%d", i)
		if _, err := conn.Prepare(ctx, stmtName, sql); err != nil {
			t.Errorf("PREPARE %s failed (real-PG parse/plan defect): %v", name, err)
		}
	}
}

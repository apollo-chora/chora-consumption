//go:build integration

package pg

// companion_suspension_projection_prepare_smoke_integration_test.go: PREPARE
// every companion_suspension_projection_repo.go SQL const against a REAL
// PostgreSQL so server-side parse analysis runs (parameter type deduction,
// column existence, cast validity, ON CONFLICT arbiter inference). Mirrors
// prepare_smoke_integration_test.go's contract (CHO-2012 lesson: unit stubs
// let a real 42P08/42703 ship green).
//
// Self-contained (own statements map + own test name), mirroring the
// atom_refresh / dose_kg_pref / companion_ritual new-files-only siblings, so
// the shared prepareSmokeStatements map is not edited here.
//
// Needs migration 0112 applied on the target DB (the table must exist for
// parse analysis). PREPARE never executes, so a read-only DSN suffices.
//
// Run:
//
//	export CHORA_TEST_DSN=postgres://...           # DB with migration 0112 applied
//	go test -tags integration -run TestCompanionSuspensionProjectionPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// companionSuspensionProjectionPrepareSmokeStatements enumerates every SQL
// const companion_suspension_projection_repo.go executes. ADD NEW CONSTS HERE
// when the adapter grows a statement.
var companionSuspensionProjectionPrepareSmokeStatements = map[string]string{
	"upsertCompanionSuspensionProjectionSQL": upsertCompanionSuspensionProjectionSQL,
	"selectEngagedCompanionSuspensionsSQL":   selectEngagedCompanionSuspensionsSQL,
}

// TestCompanionSuspensionProjectionPrepareSmoke_Parse PREPAREs every statement
// on one connection. Any 42P08 / 42703 / 42P01 / 42P10-class defect fails loud
// with the const name.
func TestCompanionSuspensionProjectionPrepareSmoke_Parse(t *testing.T) {
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
	for name, sql := range companionSuspensionProjectionPrepareSmokeStatements {
		i++
		if _, err := conn.Prepare(ctx, fmt.Sprintf("companion_suspension_projection_smoke_%d", i), sql); err != nil {
			t.Errorf("PREPARE %s failed: %v", name, err)
		}
	}
}

//go:build integration

package pg

// atom_refresh_prepare_smoke_integration_test.go: PREPARE every
// atom_refresh_ledger SQL const against a REAL PostgreSQL so server-side parse
// analysis runs (parameter type deduction, column existence, cast validity,
// ON CONFLICT partial-index arbiter inference). Mirrors
// prepare_smoke_integration_test.go's contract (CHO-2012 lesson: unit stubs
// let a real 42P08/42703 ship green).
//
// Self-contained (own statements map + own test name), mirroring the
// dose_kg_pref + companion_ritual new-files-only siblings, so the shared
// prepareSmokeStatements map is not edited here.
//
// The ON CONFLICT ... WHERE deleted_at IS NULL arbiter needs migration 0108's
// partial unique index present, so run this against a DSN whose DB has 0108
// applied.
//
// Run:
//
//	export CHORA_TEST_DSN=postgres://...           # DB with migration 0108 applied
//	go test -tags integration -run TestAtomRefreshPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// atomRefreshPrepareSmokeStatements enumerates every SQL const
// atom_refresh_ledger.go executes. ADD NEW CONSTS HERE when the adapter grows
// a statement.
var atomRefreshPrepareSmokeStatements = map[string]string{
	"claimAtomRefreshSQL":         claimAtomRefreshSQL,
	"getAtomRefreshByIdentitySQL": getAtomRefreshByIdentitySQL,
	"markAtomRefreshPublishedSQL": markAtomRefreshPublishedSQL,
	"countAtomRefreshSinceSQL":    countAtomRefreshSinceSQL,
	// goal.go grew this statement for the same D5 trigger, so it smokes here.
	"listCompanionedGoalsByTenantSQL": listCompanionedGoalsByTenantSQL,
}

// TestAtomRefreshPrepareSmoke_Parse PREPAREs every statement on one
// connection. Any 42P08 / 42703 / 42P01 / 42P10-class defect fails loud with
// the const name.
func TestAtomRefreshPrepareSmoke_Parse(t *testing.T) {
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
	for name, sql := range atomRefreshPrepareSmokeStatements {
		i++
		if _, err := conn.Prepare(ctx, fmt.Sprintf("atom_refresh_smoke_%d", i), sql); err != nil {
			t.Errorf("PREPARE %s failed: %v", name, err)
		}
	}
}

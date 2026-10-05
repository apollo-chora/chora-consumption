//go:build integration

package pg

// dose_kg_pref_prepare_smoke_integration_test.go — PREPARE every dose_kg_pref
// SQL const against a REAL PostgreSQL so server-side parse analysis runs
// (parameter type deduction, column existence, cast validity, ON CONFLICT
// partial-index arbiter inference). Mirrors prepare_smoke_integration_test.go's
// contract (CHO-2012 lesson: unit stubs let a real 42P08/42703 ship green).
//
// Self-contained (own statements map + own test name) because sub-phase B1 is
// new-files-only — the shared prepareSmokeStatements map in
// prepare_smoke_integration_test.go should also gain these consts when the repo
// is wired, but this file gives the coverage without editing that file.
//
// The ON CONFLICT ... WHERE deleted_at IS NULL arbiter needs migration 0070's
// partial unique index present, so run this against a DSN whose DB has 0070
// applied.
//
// Run:
//
//	export CHORA_TEST_DSN=postgres://...           # DB with migration 0070 applied
//	go test -tags integration -run TestDoseKGPrefPrepareSmoke ./internal/adapter/repo/pg/
import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// doseKGPrefPrepareSmokeStatements enumerates every SQL const dose_kg_pref.go
// executes. ADD NEW CONSTS HERE when the adapter grows a statement.
var doseKGPrefPrepareSmokeStatements = map[string]string{
	"upsertDoseKGPrefSQL":     upsertDoseKGPrefSQL,
	"listDoseKGPrefsSQL":      listDoseKGPrefsSQL,
	"excludedDoseKGMapIDsSQL": excludedDoseKGMapIDsSQL,
}

// TestDoseKGPrefPrepareSmoke_Parse PREPAREs every statement on one connection.
// Any 42P08 / 42703 / 42P01 / 42P10-class defect fails loud with the const name.
func TestDoseKGPrefPrepareSmoke_Parse(t *testing.T) {
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
	for name, sql := range doseKGPrefPrepareSmokeStatements {
		i++
		if _, err := conn.Prepare(ctx, fmt.Sprintf("dose_kg_pref_smoke_%d", i), sql); err != nil {
			t.Errorf("PREPARE %s failed: %v", name, err)
		}
	}
}

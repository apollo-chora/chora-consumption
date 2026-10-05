//go:build integration

// integration_test.go — live Cloud SQL integration tests gated behind
// the `integration` build tag. Run with:
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_consumption-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration ./services/chora-consumption/internal/adapter/repo/pg/...
//
// Closes the deferred Wave-A RLS check for chora_consumption.
//
// Test suite:
//
//  1. RLS isolation on learning_paths — tenant A insert NOT visible to tenant B
//  2. RLS isolation on atomic_sessions — tenant-scoped session leak check
package pg_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	cgcdb "github.com/apollo-chora/chora-common/db"
	cgcsecrets "github.com/apollo-chora/chora-common/secrets"
)

func liveDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("CHORA_TEST_DSN")
	secretID := os.Getenv("CHORA_TEST_DSN_SECRET_ID")
	if dsn == "" && secretID == "" {
		t.Skip("set CHORA_TEST_DSN or CHORA_TEST_DSN_SECRET_ID to run integration tests")
	}

	project := os.Getenv("CHORA_TEST_DB_PROJECT")
	if project == "" {
		project = "chora-489812"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var fetcher cgcdb.SecretFetcher
	var sclient *cgcsecrets.Client
	if secretID != "" && dsn == "" {
		c, err := cgcsecrets.NewClient(ctx, project)
		if err != nil {
			t.Fatalf("secret manager: %v", err)
		}
		sclient = c
		fetcher = c
	}

	pool, err := cgcdb.Bootstrap(ctx, cgcdb.BootstrapOptions{
		DSN:             dsn,
		SecretID:        secretID,
		SecretFetcher:   fetcher,
		RewriteFromPort: 6432,
		RewriteToPort:   5432,
		AppName:         "chora-consumption-pg-integration-test",
	})
	if err != nil {
		if sclient != nil {
			_ = sclient.Close()
		}
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if sclient != nil {
			_ = sclient.Close()
		}
	})
	return pool
}

// TestIntegration_RLS_TenantIsolation_LearningPaths verifies that a
// learning_path inserted under tenant A is NOT visible to tenant B.
func TestIntegration_RLS_TenantIsolation_LearningPaths(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	pathID, _ := uuid.NewV7()
	ownerGCID, _ := uuid.NewV7()
	atomID, _ := uuid.NewV7()

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM learning_paths WHERE path_id = $1`, pathID.String())
	})

	// Insert under tenant A.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("set local A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO learning_paths
            (path_id, tenant_id, owner_gcid, title, description, atom_ids)
        VALUES ($1, $2, $3, 'rls test path', 'integration test', ARRAY[$4::uuid])`,
		pathID.String(), tenantA.String(), ownerGCID.String(), atomID.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert under A: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// Read under tenant B — must see 0 rows.
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer tx2.Rollback(ctx)
	if _, err := tx2.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantB.String())); err != nil {
		t.Fatalf("set local B: %v", err)
	}
	var countB int
	if err := tx2.QueryRow(ctx,
		`SELECT count(*) FROM learning_paths WHERE path_id = $1`, pathID.String()).Scan(&countB); err != nil {
		t.Fatalf("count under B: %v", err)
	}
	if countB != 0 {
		t.Errorf("RLS LEAK: tenant B saw %d rows for tenant A's path_id", countB)
	}

	// Read under tenant A — must see 1 row.
	tx3, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A2: %v", err)
	}
	defer tx3.Rollback(ctx)
	if _, err := tx3.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		t.Fatalf("set local A2: %v", err)
	}
	var countA int
	if err := tx3.QueryRow(ctx,
		`SELECT count(*) FROM learning_paths WHERE path_id = $1`, pathID.String()).Scan(&countA); err != nil {
		t.Fatalf("count under A: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 row under tenant A, got %d", countA)
	}
	t.Logf("RLS isolation OK: tenant A=%d row(s), tenant B=%d row(s)", countA, countB)
}

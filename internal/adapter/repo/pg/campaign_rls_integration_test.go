//go:build integration

// campaign_rls_integration_test.go — WS-C0 (CHO-2079, ADR-227 D16) live RLS
// isolation gate for the 0077 campaign tables. Runs against live Cloud SQL
// via the same liveDB bootstrap as integration_test.go:
//
//	export CHORA_TEST_DSN=...   (app_rw role — RLS applies, NOBYPASSRLS)
//	go test -tags integration -run TestIntegration_CampaignRLS ./internal/adapter/repo/pg/...
//
// Acceptance (CHO-2079): two tenants with campaign rows — tenant A queries
// see zero of tenant B's rows, on BOTH new tables; goals.focus_concept_id
// exists live.
package pg_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestIntegration_CampaignRLS_NodeProgress(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	rowID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()
	conceptID, _ := uuid.NewV7()

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx)
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM campaign_node_progress WHERE id = $1`, rowID.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	// Insert a frontier row under tenant A.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("set local A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO campaign_node_progress
            (id, tenant_id, learner_gcid, concept_id, rungs_cleared, rung_cleared_at, last_advance_date)
        VALUES ($1, $2, $3, $4, 2, ARRAY[now() - interval '2 days', now() - interval '1 day'], CURRENT_DATE)`,
		rowID.String(), tenantA.String(), learnerGCID.String(), conceptID.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert under A: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// Tenant B must see zero rows.
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
		`SELECT count(*) FROM campaign_node_progress WHERE id = $1`, rowID.String()).Scan(&countB); err != nil {
		t.Fatalf("count under B: %v", err)
	}
	if countB != 0 {
		t.Errorf("RLS LEAK: tenant B saw %d campaign_node_progress rows for tenant A's id", countB)
	}

	// Tenant A must see exactly its row.
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
		`SELECT count(*) FROM campaign_node_progress WHERE id = $1`, rowID.String()).Scan(&countA); err != nil {
		t.Fatalf("count under A: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 campaign_node_progress row under tenant A, got %d", countA)
	}
	t.Logf("campaign_node_progress RLS isolation OK: A=%d, B=%d", countA, countB)
}

func TestIntegration_CampaignRLS_ConceptNodeLineage(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenantA, _ := uuid.NewV7()
	tenantB, _ := uuid.NewV7()
	rowID, _ := uuid.NewV7()
	learnerGCID, _ := uuid.NewV7()
	fromID, _ := uuid.NewV7()
	toID, _ := uuid.NewV7()

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer tx.Rollback(cctx)
		if _, err := tx.Exec(cctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
			return
		}
		if _, err := tx.Exec(cctx, `DELETE FROM concept_node_lineage WHERE id = $1`, rowID.String()); err != nil {
			return
		}
		_ = tx.Commit(cctx)
	})

	// Record a merge lineage edge under tenant A.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenantA.String())); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("set local A: %v", err)
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO concept_node_lineage
            (id, tenant_id, learner_gcid, operation, from_concept_id, to_concept_id, from_concept_key, to_concept_key)
        VALUES ($1, $2, $3, 'merge', $4, $5, 'rls-test-from', 'rls-test-to')`,
		rowID.String(), tenantA.String(), learnerGCID.String(), fromID.String(), toID.String()); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert under A: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	// Tenant B must see zero rows.
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
		`SELECT count(*) FROM concept_node_lineage WHERE id = $1`, rowID.String()).Scan(&countB); err != nil {
		t.Fatalf("count under B: %v", err)
	}
	if countB != 0 {
		t.Errorf("RLS LEAK: tenant B saw %d concept_node_lineage rows for tenant A's id", countB)
	}

	// Tenant A must see exactly its row.
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
		`SELECT count(*) FROM concept_node_lineage WHERE id = $1`, rowID.String()).Scan(&countA); err != nil {
		t.Fatalf("count under A: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 concept_node_lineage row under tenant A, got %d", countA)
	}
	t.Logf("concept_node_lineage RLS isolation OK: A=%d, B=%d", countA, countB)
}

// TestIntegration_CampaignRLS_GoalsFocusColumn is a parse-level smoke that
// goals.focus_concept_id exists live (prepare-smoke spirit — the C1 repo SQL
// consts join prepareSmokeStatements when they land).
func TestIntegration_CampaignRLS_GoalsFocusColumn(t *testing.T) {
	pool := liveDB(t)
	ctx := context.Background()

	tenant, _ := uuid.NewV7()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL chora.tenant_id = '%s'", tenant.String())); err != nil {
		t.Fatalf("set local: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(focus_concept_id) FROM goals WHERE false`).Scan(&n); err != nil {
		t.Fatalf("goals.focus_concept_id not selectable: %v", err)
	}
}

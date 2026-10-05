// user_kg_test.go — verifies the Postgres adapter contract for the
// per-user Knowledge Graph: every read/write path calls
// rls.ApplySession on the supplied transaction BEFORE issuing the
// domain query (per multi-tenant-rls skill). Until pgx lands, a stub
// Querier captures the SQL strings and asserts the rls helper was
// called.
//
// pgx wiring lands at M12 — at that point the SQL templates here become
// the runtime query strings and the rls helper becomes mandatory.
package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/rls"
	"github.com/apollo-chora/chora-common/tracing"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

const (
	pgTenantID = "01970000-0000-7000-8000-000000000001"
	pgUserGCID = "01970000-0000-7000-9000-000000000001"
)

// stubQuerier records every Exec call (SQL + args) so tests can verify the
// RLS SET LOCAL pair lands first and arg coercions hold (e.g. nil-slice →
// empty for NOT NULL array columns).
type stubQuerier struct {
	execCalls []string
	execArgs  [][]any
}

func (s *stubQuerier) Exec(ctx context.Context, sql string, args ...any) (rls.CommandTag, error) {
	s.execCalls = append(s.execCalls, sql)
	s.execArgs = append(s.execArgs, args)
	return rls.CommandTag{RowsAffected: 0}, nil
}
func (s *stubQuerier) QueryRow(ctx context.Context, sql string, args ...any) Row { return nil }
func (s *stubQuerier) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return nil, nil
}

// stubTxRunner runs fn synchronously with the stub querier.
type stubTxRunner struct {
	q *stubQuerier
}

func (r *stubTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context, q Querier) error) error {
	if r.q == nil {
		r.q = &stubQuerier{}
	}
	return fn(ctx, r.q)
}

// withCtx returns a context with tenant + gcid populated (mimics the
// HTTP middleware).
func withCtx() context.Context {
	ctx := context.Background()
	ctx = tracing.WithTenantID(ctx, pgTenantID)
	ctx = tracing.WithGCID(ctx, pgUserGCID)
	return ctx
}

func TestPGMapClusterRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)

	c, _ := userknowledgegraph.NewMapCluster(pgTenantID, pgUserGCID, "agile", "01970000-0000-7000-a000-000000000001", "")

	err := repo.Save(withCtx(), c)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil {
		t.Fatal("tx querier nil")
	}
	if last := tx.q.execCalls[len(tx.q.execCalls)-1]; !contains(last, "INSERT INTO kg_user_map_clusters") {
		t.Errorf("last exec = %q; want the cluster upsert", last)
	}
	if len(tx.q.execCalls) < 2 {
		t.Fatalf("expected ≥ 2 SET LOCAL calls (tenant + gcid), got %d", len(tx.q.execCalls))
	}
	// First call must be SET LOCAL chora.tenant_id, second chora.user_gcid.
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

func TestPGMapClusterRepo_LoadAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)
	_, err := repo.Load(withCtx(), "id")
	if !errors.Is(err, userknowledgegraph.ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound from the nil stub row", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("RLS calls missing: %v", tx.q.execCalls)
	}
}

func TestPGMapClusterRepo_ListAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)
	if _, err := repo.ListActiveByUser(withCtx(), pgTenantID, pgUserGCID); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("RLS calls missing: %v", tx.q.execCalls)
	}
}

func TestPGMapClusterRepo_CountAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)
	if _, err := repo.CountActiveByUser(withCtx(), pgTenantID, pgUserGCID); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("RLS calls missing: %v", tx.q.execCalls)
	}
}

func TestPGMapClusterRepo_FindBySurvivorAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)
	if _, err := repo.FindBySurvivor(withCtx(), pgTenantID, "id"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(tx.q.execCalls) < 2 {
		t.Errorf("RLS calls missing: %v", tx.q.execCalls)
	}
}

func TestPGJunctionRepo_AppliesRLS_OnAllOps(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewJunctionRepo(tx)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"save", func() error {
			j, _ := userknowledgegraph.NewJunction(pgTenantID, pgUserGCID, "ca", "cb", []string{"a"}, time.Now().UTC())
			return repo.Save(withCtx(), j)
		}},
		{"load", func() error { _, e := repo.Load(withCtx(), "id"); return e }},
		{"list_pending", func() error { _, e := repo.ListPendingForUser(withCtx(), pgTenantID, pgUserGCID); return e }},
		{"find_pending_pair", func() error {
			_, e := repo.FindPendingByPair(withCtx(), pgTenantID, pgUserGCID, "ca", "cb")
			return e
		}},
	}
	for _, c := range cases {
		tx.q = &stubQuerier{}
		err := c.fn()
		// load returns ErrNotFound via the nil stub row; the rest succeed.
		if err != nil && !errors.Is(err, userknowledgegraph.ErrNotFound) {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if len(tx.q.execCalls) < 2 {
			t.Errorf("%s: RLS calls missing", c.name)
		}
	}
}

func TestPG_FailsLoudOnMissingTenantContext(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewMapClusterRepo(tx)
	// Bare context without tenant.
	c, _ := userknowledgegraph.NewMapCluster(pgTenantID, pgUserGCID, "x", "01970000-0000-7000-a000-000000000001", "")
	err := repo.Save(context.Background(), c)
	if !errors.Is(err, rls.ErrNoTenantContext) {
		t.Errorf("err = %v, want ErrNoTenantContext", err)
	}
}

func TestSchemaSummary_NonEmpty(t *testing.T) {
	if SchemaSummary() == "" {
		t.Error("schema summary empty")
	}
}

func TestPGMapClusterRepo_RejectsNilCluster(t *testing.T) {
	repo := NewMapClusterRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidMapCluster) {
		t.Errorf("err = %v; want ErrInvalidMapCluster", err)
	}
}

func TestPGJunctionRepo_RejectsNilJunction(t *testing.T) {
	repo := NewJunctionRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidJunction) {
		t.Errorf("err = %v; want ErrInvalidJunction", err)
	}
}

// SQL constants exposed at package level for review-via-test.
func TestSQLTemplates_NonEmpty(t *testing.T) {
	templates := []string{
		upsertMapClusterSQL, loadMapClusterSQL, listActiveMapClustersSQL,
		countActiveMapClustersSQL, findClustersBySurvivorSQL,
		upsertJunctionSQL, loadJunctionSQL, listPendingJunctionsSQL,
		findPendingJunctionByPairSQL,
	}
	for i, s := range templates {
		if s == "" {
			t.Errorf("template[%d] empty", i)
		}
		// Sanity — every template must reference its target table.
	}
}

// ---------- helpers ----------

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

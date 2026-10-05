// cluster_projection_test.go — arg-binding regression for the ADR-223
// MapCluster→Goal projection applier (ClusterProjectionApplier).
//
// The applier reuses the shared insertConceptNodeSQL const. When CHO-2038
// (migration 0066) added the `intent` column at $7, every caller had to bind a
// 9th arg — but this applier's call site (committed earlier, deploy held) was
// NOT updated and passed only 8 args to the 9-placeholder INSERT. That is a
// runtime pgx arg-count error (500 PROJECTION_FAILED), invisible to unit tests
// because the applier had NO direct pg-layer coverage and the prepare-smoke
// test only PREPAREs the SQL (it cannot see the Go call's arg count).
//
// This test pins the bound-arg count for each write so shared-const schema
// drift fails loud at unit time, not in production.
package pg

import (
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

func TestPGClusterProjectionApplier_BindsIntentAndOneTx(t *testing.T) {
	rq := &recordingQuerier{}
	applier := NewClusterProjectionApplier(&recordingTxRunner{q: rq})

	root := &conceptgraph.ConceptNode{
		ConceptID: "01970000-0000-7000-c000-0000000000f1", TenantID: pgTenantID,
		LearnerGCID: pgUserGCID, Title: "Fractions", AtomRefs: []string{},
		CreatedAt: cgNow, UpdatedAt: cgNow,
	}
	g := &goal.Goal{
		GoalID: "01970000-0000-7000-9000-0000000000f2", TenantID: pgTenantID,
		LearnerGCID: pgUserGCID, ConceptSet: []string{}, RootConceptID: &root.ConceptID,
		CreatedAt: cgNow, UpdatedAt: cgNow,
	}
	cluster := &userknowledgegraph.MapCluster{
		ClusterID: "01970000-0000-7000-c000-0000000000f3", TenantID: pgTenantID,
		UserGCID: pgUserGCID, DisplayName: "Fractions", ProjectedIntoGoalID: g.GoalID,
		NodeCount: 1, Version: 1, CreatedAt: cgNow, UpdatedAt: cgNow,
	}

	if err := applier.Apply(withCtx(), root, g, cluster); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Locate each mutation by SQL, then assert its bound-arg count matches the
	// canonical const's placeholder count.
	find := func(needle string) []any {
		for i, sql := range rq.execSQL {
			if contains(sql, needle) {
				return rq.execArgs[i]
			}
		}
		t.Fatalf("no exec matching %q; recorded SQL %v", needle, rq.execSQL)
		return nil
	}

	// DERIVE the expectation from the canonical const — do not hardcode it.
	// This assertion previously pinned a literal 10 while insertConceptNodeSQL
	// had grown to 12 placeholders (sub_goal + sub_goal_provenance), so it went
	// on passing while every write through this path failed in production with
	// "expected 12 parameters but got 10".
	wantArgs := maxPlaceholder(insertConceptNodeSQL)
	if got := len(find("INSERT INTO concept_nodes")); got != wantArgs {
		t.Fatalf("concept insert bound args = %d; want %d (insertConceptNodeSQL placeholder count)", got, wantArgs)
	}
	if got := len(find("INSERT INTO")); got == 0 { // sanity: goal insert present
		t.Fatalf("no goal insert recorded")
	}

	// All three writes ride ONE RunInTx (single RLS SET LOCAL pair), so no
	// Goal is ever committed without its root concept + cluster flip.
	if !contains(rq.execSQL[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("RLS SET LOCAL must precede the writes; got %v", rq.execSQL)
	}
}

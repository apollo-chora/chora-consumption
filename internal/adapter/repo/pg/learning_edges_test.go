package pg

import (
	"errors"
	"testing"

	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
)

var errLEApply = errors.New("learning-edge apply boom")

// countSQL counts captured statements containing needle.
func countSQL(sqls []string, needle string) int {
	n := 0
	for _, s := range sqls {
		if contains(s, needle) {
			n++
		}
	}
	return n
}

func mintTwo(t *testing.T) []conceptgraph.MintedLearningEdge {
	t.Helper()
	minted, err := conceptgraph.SelectLearningEdges(conceptgraph.SelectLearningEdgesInput{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID, RootConceptID: cgConcept,
		Selections: []conceptgraph.LearningEdgeSelection{
			{Title: "Adding fractions", Intent: conceptgraph.IntentRemediate},
			{Title: "Continued fractions", Intent: conceptgraph.IntentExplore},
		},
		Now: cgNow,
	})
	if err != nil {
		t.Fatalf("SelectLearningEdges: %v", err)
	}
	return minted
}

func TestLearningEdgeRepo_ApplyPersistsNodesAndEdgesInOneTx(t *testing.T) {
	fake := &goalFakeQuerier{}
	repo := NewLearningEdgeRepo(&stubTxRunnerWith{q: fake})

	if err := repo.ApplyLearningEdges(withCtx(), mintTwo(t)); err != nil {
		t.Fatalf("ApplyLearningEdges: %v", err)
	}
	// Both minted pairs persisted: 2 concept inserts + 2 edge inserts, one tx.
	if got := countSQL(fake.sqls, "INSERT INTO concept_nodes"); got != 2 {
		t.Errorf("concept_nodes inserts = %d; want 2", got)
	}
	if got := countSQL(fake.sqls, "INSERT INTO concept_edges"); got != 2 {
		t.Errorf("concept_edges inserts = %d; want 2", got)
	}
	// RLS applied before the writes (SET LOCAL chora.tenant_id ...).
	if countSQL(fake.sqls, "SET LOCAL") == 0 {
		t.Error("RLS ApplySession must run before the batch writes")
	}
}

func TestLearningEdgeRepo_EmptyIsNoop(t *testing.T) {
	fake := &goalFakeQuerier{}
	repo := NewLearningEdgeRepo(&stubTxRunnerWith{q: fake})
	if err := repo.ApplyLearningEdges(withCtx(), nil); err != nil {
		t.Fatalf("empty ApplyLearningEdges: %v", err)
	}
	if len(fake.sqls) != 0 {
		t.Errorf("empty batch must not open a tx / run SQL; got %v", fake.sqls)
	}
}

func TestLearningEdgeRepo_InsertErrorPropagates(t *testing.T) {
	fake := &goalFakeQuerier{execErr: errLEApply}
	repo := NewLearningEdgeRepo(&stubTxRunnerWith{q: fake})
	if err := repo.ApplyLearningEdges(withCtx(), mintTwo(t)); err == nil {
		t.Fatal("a failed insert must propagate (whole batch rolls back)")
	}
}

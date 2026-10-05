// atom_semantic_edge_test.go — RLS contract verification for the
// atom_semantic_edges pgx adapter.
package pg

import (
	"errors"
	"testing"
	"time"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

func TestPGAtomSemanticEdgesRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewAtomSemanticEdgesRepo(tx)

	e, err := userknowledgegraph.NewAtomSemanticEdge(
		pgTenantID,
		"01970000-0000-7000-a000-000000000001",
		"01970000-0000-7000-a000-000000000002",
		userknowledgegraph.AtomEdgeTypePrerequisiteOf,
		0.8,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("NewAtomSemanticEdge: %v", err)
	}
	if err := repo.Save(withCtx(), e); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO atom_semantic_edges") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGAtomSemanticEdgesRepo_SaveRejectsNil(t *testing.T) {
	repo := NewAtomSemanticEdgesRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidAtomSemanticEdge) {
		t.Errorf("err = %v; want ErrInvalidAtomSemanticEdge", err)
	}
}

func TestPGAtomSemanticEdgesRepo_FindByAtomsAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomSemanticEdgesRepo(tx)
	_, _ = repo.FindByAtoms(withCtx(), pgTenantID, "atom-a", "atom-b")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGAtomSemanticEdgesRepo_ListBySourceAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewAtomSemanticEdgesRepo(tx)
	_, _ = repo.ListBySource(withCtx(), pgTenantID, "atom-a", 10)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

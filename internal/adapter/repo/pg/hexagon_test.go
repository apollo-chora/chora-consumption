// hexagon_test.go — RLS contract verification for the HexagonNode pgx
// adapter.
package pg

import (
	"errors"
	"testing"

	userknowledgegraph "github.com/apollo-chora/chora-consumption/internal/domain/user_knowledge_graph"
)

func TestPGHexagonRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewHexagonRepo(tx)

	neighbors := make([]userknowledgegraph.HexagonNeighbor, 6)
	for i := 0; i < 6; i++ {
		neighbors[i] = userknowledgegraph.HexagonNeighbor{
			AtomID:     "01970000-0000-7000-a000-00000000010" + string(rune('1'+i)),
			Relation:   userknowledgegraph.NeighborRelationExtends,
			Confidence: 0.7,
			FogLabel:   "neighbor",
		}
	}
	h, err := userknowledgegraph.NewHexagonNode(
		"01970000-0000-7000-a000-000000000001", // cluster
		"01970000-0000-7000-a000-000000000002", // exploration
		pgTenantID, pgUserGCID,
		"01970000-0000-7000-a000-000000000003", // focal atom
		neighbors,
		"run-1", "model-1",
	)
	if err != nil {
		t.Fatalf("NewHexagonNode: %v", err)
	}
	if err := repo.Save(withCtx(), h); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO kg_hexagon_nodes") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGHexagonRepo_SaveRejectsNil(t *testing.T) {
	repo := NewHexagonRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidHexagonNode) {
		t.Errorf("err = %v; want ErrInvalidHexagonNode", err)
	}
}

func TestPGHexagonRepo_FindFreshAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewHexagonRepo(tx)
	_, _ = repo.FindFresh(withCtx(), "exp-1", "atom-1")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

// learning_path_provenance_test.go — RED-phase tests for the ADR-233 provenance
// axis on the pg LearningPath adapter (migration 0093).
//
// The upsert must PERSIST the provenance + traversal + dedupe-anchor columns —
// a Save that silently drops them would leave every derived study list looking
// ad-hoc/linear on reload, which is precisely the silent-nonsense ADR-233 exists
// to prevent.
package pg

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	pgCollectionID  = "01970000-0000-7000-b000-000000000001"
	pgStudyListEvID = "01970000-0000-7000-c000-000000000001"
)

// The upsert must carry the 0093 columns.
func TestPGLearningPathRepo_SavePersistsProvenanceColumns(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearningPathRepo(tx)

	p, err := learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID:         pgTenantID,
		OwnerGCID:        pgUserGCID,
		CollectionID:     pgCollectionID,
		AtomIDs:          []string{"01970000-0000-7000-a000-000000000001"},
		StudyListEventID: pgStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	if err := repo.Save(withCtx(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	for _, col := range []string{"source_type", "source_id", "traversal_mode", "study_list_event_id"} {
		if !contains(last, col) {
			t.Errorf("upsert SQL missing %q column (ADR-233 D2/D3); got %q", col, last)
		}
	}
}

// GetBySourceCollection — the subscriber's get-or-create probe. Must open the
// RLS session (SET LOCAL pair) before the SELECT.
func TestPGLearningPathRepo_GetBySourceCollectionAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearningPathRepo(tx)
	_, _ = repo.GetBySourceCollection(withCtx(), pgTenantID, pgUserGCID, pgCollectionID)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair (RLS session); got %d", len(tx.q.execCalls))
	}
}

// GetByStudyListEventID — the DURABLE delivery-dedupe anchor lookup.
func TestPGLearningPathRepo_GetByStudyListEventIDAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearningPathRepo(tx)
	_, _ = repo.GetByStudyListEventID(withCtx(), pgStudyListEvID)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair (RLS session); got %d", len(tx.q.execCalls))
	}
}

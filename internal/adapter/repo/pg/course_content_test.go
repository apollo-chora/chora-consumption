// course_content_test.go — RLS contract verification for the course_content
// pgx adapter (CHO-1612 D2 durability). Stub Querier captures SQL strings.
//
// Mirrors atom_index_test.go: the stub records Exec() SQL so we can assert the
// SET LOCAL chora.tenant_id pair lands BEFORE the projection mutation, and that
// ReplaceByCourse issues the DELETE-then-INSERT snapshot swap.
package pg

import (
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

func ccItem(t *testing.T, itemID, courseID string, pos int) *course_content.Item {
	t.Helper()
	it, err := course_content.New(course_content.NewParams{
		ItemID:   itemID,
		TenantID: pgTenantID,
		CourseID: courseID,
		Kind:     course_content.KindAtom,
		Ref:      "01970000-0000-7000-c000-00000000000" + itemID[len(itemID)-1:],
		Title:    "Item " + itemID,
		Position: pos,
	})
	if err != nil {
		t.Fatalf("ccItem: %v", err)
	}
	return it
}

func TestPGCourseContentRepo_ReplaceByCourseAppliesRLSAndSwaps(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewCourseContentRepo(tx)
	courseID := "01970000-0000-7000-b000-000000000001"
	items := []*course_content.Item{
		ccItem(t, "01970000-0000-7000-a000-000000000001", courseID, 0),
		ccItem(t, "01970000-0000-7000-a000-000000000002", courseID, 1),
	}
	if err := repo.ReplaceByCourse(withCtx(), pgTenantID, courseID, items); err != nil {
		t.Fatalf("ReplaceByCourse: %v", err)
	}
	calls := tx.q.execCalls
	// Expect: SET LOCAL tenant, SET LOCAL gcid, tombstone UPDATE, then ≥1 upsert.
	if len(calls) < 4 {
		t.Fatalf("expected RLS pair + tombstone + upsert(s); got %d: %v", len(calls), calls)
	}
	if !contains(calls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q; want SET LOCAL tenant", calls[0])
	}
	if !contains(calls[2], "UPDATE course_content SET deleted_at") {
		t.Errorf("call[2] = %q; want tombstone UPDATE", calls[2])
	}
	if !contains(calls[len(calls)-1], "INSERT INTO course_content") {
		t.Errorf("last call = %q; want upsert INSERT", calls[len(calls)-1])
	}
}

// ReplaceByCourse with an empty set must still tombstone (clearing the course)
// but issue no upsert — the "course content removed" snapshot case.
func TestPGCourseContentRepo_ReplaceByCourseEmptyTombstonesOnly(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewCourseContentRepo(tx)
	if err := repo.ReplaceByCourse(withCtx(), pgTenantID, "01970000-0000-7000-b000-000000000009", nil); err != nil {
		t.Fatalf("ReplaceByCourse(empty): %v", err)
	}
	for _, c := range tx.q.execCalls {
		if contains(c, "INSERT INTO course_content") {
			t.Errorf("unexpected upsert on empty replace: %v", tx.q.execCalls)
		}
	}
	if len(tx.q.execCalls) < 3 || !contains(tx.q.execCalls[2], "UPDATE course_content SET deleted_at") {
		t.Errorf("expected RLS pair + tombstone; got %v", tx.q.execCalls)
	}
}

func TestPGCourseContentRepo_ListByCourseAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewCourseContentRepo(tx)
	_, _ = repo.ListByCourse(withCtx(), pgTenantID, "01970000-0000-7000-b000-000000000001")
	if len(tx.q.execCalls) < 2 {
		t.Fatalf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
	if !contains(tx.q.execCalls[0], "SET LOCAL chora.tenant_id") {
		t.Errorf("call[0] = %q", tx.q.execCalls[0])
	}
	if !contains(tx.q.execCalls[1], "SET LOCAL chora.user_gcid") {
		t.Errorf("call[1] = %q", tx.q.execCalls[1])
	}
}

// learning_path_test.go — RLS contract verification.
package pg

import (
	"errors"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

func TestPGLearningPathRepo_SaveAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearningPathRepo(tx)

	p, err := learning_path.New(pgTenantID, pgUserGCID, "Path-1", []string{"01970000-0000-7000-a000-000000000001"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := repo.Save(withCtx(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if tx.q == nil || len(tx.q.execCalls) < 3 {
		t.Fatalf("expected SET LOCAL pair + INSERT; got %v", tx.q)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	if !contains(last, "INSERT INTO learning_paths") {
		t.Errorf("last exec call = %q", last)
	}
}

func TestPGLearningPathRepo_SaveRejectsNil(t *testing.T) {
	repo := NewLearningPathRepo(&stubTxRunner{})
	if err := repo.Save(withCtx(), nil); !errors.Is(err, ErrInvalidLearningPath) {
		t.Errorf("err = %v; want ErrInvalidLearningPath", err)
	}
}

func TestPGLearningPathRepo_GetAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearningPathRepo(tx)
	_, _ = repo.Get(withCtx(), "01970000-0000-7000-d000-000000000001")
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGLearningPathRepo_ListByLearnerAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearningPathRepo(tx)
	_, _ = repo.ListByLearner(withCtx(), pgTenantID, pgUserGCID, 0)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

// R3: the upsert must persist the course-binding + cursor columns added in
// migration 0043 so a restart-surviving path is course-scoped + resumable.
func TestPGLearningPathRepo_SavePersistsCourseBindingColumns(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearningPathRepo(tx)
	p, _ := learning_path.New(pgTenantID, pgUserGCID, "Path-1", []string{"01970000-0000-7000-a000-000000000001"})
	p.CourseID = "01970000-0000-7000-b000-000000000001"
	p.EnrollmentID = "01970000-0000-7000-c000-000000000001"
	if err := repo.Save(withCtx(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	last := tx.q.execCalls[len(tx.q.execCalls)-1]
	for _, col := range []string{"course_id", "enrollment_id", "current_index"} {
		if !contains(last, col) {
			t.Errorf("upsert SQL missing %q column; got %q", col, last)
		}
	}
}

// R3: a 0-atom path (content-empty course, OPEN-1) must be persistable — the
// pg adapter no longer relies on the dropped cardinality(atom_ids)>=1 CHECK.
func TestPGLearningPathRepo_SaveAllowsZeroAtomPath(t *testing.T) {
	tx := &stubTxRunner{}
	repo := NewLearningPathRepo(tx)
	p, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: pgTenantID, LearnerGCID: pgUserGCID,
		CourseID: "01970000-0000-7000-b000-000000000001", AtomIDs: nil,
	})
	if err != nil {
		t.Fatalf("bootstrap 0-atom: %v", err)
	}
	if err := repo.Save(withCtx(), p); err != nil {
		t.Fatalf("Save 0-atom path: %v", err)
	}
}

func TestPGLearningPathRepo_GetByCourseAndGCIDAppliesRLS(t *testing.T) {
	tx := &stubTxRunner{q: &stubQuerier{}}
	repo := NewLearningPathRepo(tx)
	_, _ = repo.GetByCourseAndGCID(withCtx(), pgTenantID, "course-1", pgUserGCID)
	if len(tx.q.execCalls) < 2 {
		t.Errorf("expected SET LOCAL pair; got %d", len(tx.q.execCalls))
	}
}

func TestPGLearningPathRepo_ListByCourseAndWithAtomApplyRLS(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*LearningPathRepo)
	}{
		{"ListByCourse", func(r *LearningPathRepo) { _, _ = r.ListByCourse(withCtx(), pgTenantID, "course-1") }},
		{"ListByLearnerWithAtom", func(r *LearningPathRepo) {
			_, _ = r.ListByLearnerWithAtom(withCtx(), pgTenantID, pgUserGCID, "atom-1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &stubTxRunner{q: &stubQuerier{}}
			tc.call(NewLearningPathRepo(tx))
			if len(tx.q.execCalls) < 2 {
				t.Errorf("%s: expected SET LOCAL pair; got %d", tc.name, len(tx.q.execCalls))
			}
		})
	}
}

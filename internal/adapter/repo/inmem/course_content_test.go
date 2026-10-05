package inmem

import (
	"context"
	"testing"

	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

func item(id string, pos int) *course_content.Item {
	it, _ := course_content.New(course_content.NewParams{
		ItemID: id, TenantID: "t1", CourseID: "c1", Kind: course_content.KindAtom,
		Ref: "019e30db-0000-7000-8000-0000000000a1", Title: id, Position: pos,
	})
	return it
}

func TestCourseContentRepo_ReplaceAndListOrdered(t *testing.T) {
	r := NewCourseContentRepo()
	// insert out of order; expect ordered-by-position read
	if err := r.ReplaceByCourse(context.Background(), "t1", "c1", []*course_content.Item{item("b", 1), item("a", 0)}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := r.ListByCourse(context.Background(), "t1", "c1")
	if len(got) != 2 || got[0].ItemID != "a" || got[1].ItemID != "b" {
		t.Fatalf("not ordered by position: %+v", got)
	}
}

func TestCourseContentRepo_ReplaceSwapsWholeSet(t *testing.T) {
	r := NewCourseContentRepo()
	_ = r.ReplaceByCourse(context.Background(), "t1", "c1", []*course_content.Item{item("a", 0), item("b", 1)})
	_ = r.ReplaceByCourse(context.Background(), "t1", "c1", []*course_content.Item{item("c", 0)})
	got, _ := r.ListByCourse(context.Background(), "t1", "c1")
	if len(got) != 1 || got[0].ItemID != "c" {
		t.Fatalf("replace did not swap whole set: %+v", got)
	}
}

func TestCourseContentRepo_EmptyClears(t *testing.T) {
	r := NewCourseContentRepo()
	_ = r.ReplaceByCourse(context.Background(), "t1", "c1", []*course_content.Item{item("a", 0)})
	_ = r.ReplaceByCourse(context.Background(), "t1", "c1", nil)
	got, _ := r.ListByCourse(context.Background(), "t1", "c1")
	if len(got) != 0 {
		t.Fatalf("empty replace should clear, got %d", len(got))
	}
}

func TestCourseContentRepo_TenantIsolation(t *testing.T) {
	r := NewCourseContentRepo()
	_ = r.ReplaceByCourse(context.Background(), "t1", "c1", []*course_content.Item{item("a", 0)})
	got, _ := r.ListByCourse(context.Background(), "other", "c1")
	if len(got) != 0 {
		t.Fatalf("cross-tenant read must be empty, got %d", len(got))
	}
}

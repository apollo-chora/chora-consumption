// atom_index_test.go — RED phase tests for the in-memory atom_index repo.
//
// The repo serves the cross-DB-forbidden projection of LearningAtom data
// hydrated from `chora.creation.atom.created.v1`. Tests cover Save +
// GetByAtomID + ListByCourse + ListByTopic + soft-delete behaviour.
//
// The repo now implements the ctx-taking atom_index.Repo port (CHO-1612 D2);
// the in-memory adapter ignores ctx, so tests pass context.Background().
package inmem

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

func TestAtomIndexRepo_SaveAndGet(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, err := atom_index.New(atom_index.NewParams{
		AtomID:      "atom-1",
		TenantID:    "t1",
		CourseID:    "course-1",
		Title:       "MCQ Atom",
		AtomType:    "mcq",
		TopicTags:   []string{"agile"},
		PublishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := r.Get(ctx, "atom-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AtomID != "atom-1" {
		t.Errorf("AtomID = %q", got.AtomID)
	}
}

// CHO-1968 hardening: an out-of-order late atom.created (draft, EMPTY answer key)
// upserting AFTER atom.published already flipped the row must NOT clobber the
// published row's authoritative answer key. Both events can queue during a
// cost-pause and redeliver out of order across two separate push subscriptions
// (no cross-subscription ordering). Mirrors the pg upsert preserve-when-published
// CASE so pg + inmem stay behaviourally identical.
func TestAtomIndexRepo_LateDraftSavePreservesPublishedAnswerKey(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// atom.published already flipped the row: PUBLISHED + authoritative key.
	published, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-x", TenantID: "t1", AtomType: "mcq",
		CorrectOptionID: "opt_1", AnswerCount: 2,
		Status: atom_index.StatusPublished, PublishedAt: now,
	})
	if err := r.Save(ctx, published); err != nil {
		t.Fatalf("Save published: %v", err)
	}

	// A LATE atom.created replays out of order: draft, EMPTY key.
	lateDraft, _ := atom_index.New(atom_index.NewParams{
		AtomID: "atom-x", TenantID: "t1", AtomType: "mcq",
		Status: atom_index.StatusDraft, PublishedAt: now,
	})
	if err := r.Save(ctx, lateDraft); err != nil {
		t.Fatalf("Save late draft: %v", err)
	}

	got, err := r.Get(ctx, "atom-x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != atom_index.StatusPublished {
		t.Errorf("Status = %q; want published (non-downgrading)", got.Status)
	}
	if got.CorrectOptionID != "opt_1" {
		t.Errorf("CorrectOptionID = %q; want opt_1 preserved (late draft must not clobber the published key)", got.CorrectOptionID)
	}
	if got.AnswerCount != 2 {
		t.Errorf("AnswerCount = %d; want 2 preserved", got.AnswerCount)
	}
	if !got.IsMCQ() {
		t.Errorf("IsMCQ() = false; the atom silently dropped from grading after a late draft replay")
	}
}

func TestAtomIndexRepo_GetMissingReturnsNotFound(t *testing.T) {
	r := NewAtomIndexRepo()
	_, err := r.Get(context.Background(), "nope")
	if !errors.Is(err, atom_index.ErrNotFound) {
		t.Errorf("err = %v; want atom_index.ErrNotFound", err)
	}
}

func TestAtomIndexRepo_SoftDeletedExcludedFromGet(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, _ := atom_index.New(atom_index.NewParams{AtomID: "a", TenantID: "t1", PublishedAt: now})
	a.SoftDelete(now)
	_ = r.Save(ctx, a)
	if _, err := r.Get(ctx, "a"); !errors.Is(err, atom_index.ErrNotFound) {
		t.Errorf("expected atom_index.ErrNotFound on soft-deleted; got %v", err)
	}
}

func TestAtomIndexRepo_ListByCourse_FiltersByTenantAndCourse(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	mk := func(id, tenant, course string) *atom_index.AtomIndex {
		a, _ := atom_index.New(atom_index.NewParams{
			AtomID: id, TenantID: tenant, CourseID: course, PublishedAt: now,
		})
		return a
	}
	_ = r.Save(ctx, mk("a1", "t1", "c1"))
	_ = r.Save(ctx, mk("a2", "t1", "c1"))
	_ = r.Save(ctx, mk("a3", "t1", "c2"))
	_ = r.Save(ctx, mk("a4", "t2", "c1"))

	got, err := r.ListByCourse(ctx, "t1", "c1")
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d; want 2 (t1 c1)", len(got))
	}
}

func TestAtomIndexRepo_ListByCourse_ExcludesSoftDeleted(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID: "a", TenantID: "t1", CourseID: "c1", PublishedAt: now,
	})
	a.SoftDelete(now)
	_ = r.Save(ctx, a)
	got, err := r.ListByCourse(ctx, "t1", "c1")
	if err != nil {
		t.Fatalf("ListByCourse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d; want 0", len(got))
	}
}

// --- SearchForLearner (real RAG source for the AI Kernel recommender) ---

func mkAtomAt(id, tenant, topic string, ts time.Time) *atom_index.AtomIndex {
	tags := []string{}
	if topic != "" {
		tags = []string{topic}
	}
	a, _ := atom_index.New(atom_index.NewParams{
		AtomID: id, TenantID: tenant, CourseID: "c1", Title: id + " title",
		AtomType: "mcq", Difficulty: 2, TopicTags: tags, PublishedAt: ts,
	})
	return a
}

func TestAtomIndexRepo_SearchForLearner_FiltersByTenant(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	_ = r.Save(ctx, mkAtomAt("t1-a", "t1", "agile", base))
	_ = r.Save(ctx, mkAtomAt("t1-b", "t1", "scrum", base))
	_ = r.Save(ctx, mkAtomAt("t2-a", "t2", "agile", base))

	got, err := r.SearchForLearner(ctx, "t1", "", 5)
	if err != nil {
		t.Fatalf("SearchForLearner: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2 (only t1)", len(got))
	}
	for _, a := range got {
		if a.TenantID != "t1" {
			t.Errorf("leaked tenant %q", a.TenantID)
		}
	}
}

func TestAtomIndexRepo_SearchForLearner_OrdersByRecencyDesc(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	_ = r.Save(ctx, mkAtomAt("oldest", "t1", "agile", base))
	_ = r.Save(ctx, mkAtomAt("newest", "t1", "agile", base.Add(48*time.Hour)))
	_ = r.Save(ctx, mkAtomAt("middle", "t1", "agile", base.Add(24*time.Hour)))

	got, err := r.SearchForLearner(ctx, "t1", "", 5)
	if err != nil {
		t.Fatalf("SearchForLearner: %v", err)
	}
	want := []string{"newest", "middle", "oldest"}
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3", len(got))
	}
	for i, id := range want {
		if got[i].AtomID != id {
			t.Errorf("got[%d] = %q; want %q", i, got[i].AtomID, id)
		}
	}
}

func TestAtomIndexRepo_SearchForLearner_TopicHintBiasesMatchesFirst(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	// scrum atom is OLDER than the agile atoms, but a "scrum" hint must surface
	// it first (topic bias overrides pure recency for matches).
	_ = r.Save(ctx, mkAtomAt("agile-new", "t1", "agile", base.Add(48*time.Hour)))
	_ = r.Save(ctx, mkAtomAt("agile-old", "t1", "agile", base.Add(24*time.Hour)))
	_ = r.Save(ctx, mkAtomAt("scrum-oldest", "t1", "scrum", base))

	got, err := r.SearchForLearner(ctx, "t1", "scrum", 5)
	if err != nil {
		t.Fatalf("SearchForLearner: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d; want 3 (match + top-up)", len(got))
	}
	if got[0].AtomID != "scrum-oldest" {
		t.Errorf("got[0] = %q; want scrum-oldest (topic match first)", got[0].AtomID)
	}
}

func TestAtomIndexRepo_SearchForLearner_RespectsLimit(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		_ = r.Save(ctx, mkAtomAt(string(rune('a'+i)), "t1", "agile", base.Add(time.Duration(i)*time.Hour)))
	}
	got, err := r.SearchForLearner(ctx, "t1", "", 3)
	if err != nil {
		t.Fatalf("SearchForLearner: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d; want 3 (limit)", len(got))
	}
}

func TestAtomIndexRepo_SearchForLearner_ExcludesSoftDeleted(t *testing.T) {
	r := NewAtomIndexRepo()
	ctx := context.Background()
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	live := mkAtomAt("live", "t1", "agile", base)
	dead := mkAtomAt("dead", "t1", "agile", base.Add(time.Hour))
	dead.SoftDelete(base)
	_ = r.Save(ctx, live)
	_ = r.Save(ctx, dead)

	got, err := r.SearchForLearner(ctx, "t1", "", 5)
	if err != nil {
		t.Fatalf("SearchForLearner: %v", err)
	}
	if len(got) != 1 || got[0].AtomID != "live" {
		t.Errorf("got %v; want [live]", got)
	}
}

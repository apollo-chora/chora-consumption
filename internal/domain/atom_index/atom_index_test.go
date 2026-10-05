// Package atom_index — tests for the skinny atom-index projection hydrated
// from `chora.creation.atom.created.v1`. The atom_index does NOT hold full
// atom content (cross-DB-forbidden + atom-centric rule) — only the metadata
// Phyllis needs for fast lookup and identity-based MCQ answer-key resolution
// (CHO-1627: grade by stable option_id, NOT positional index).
package atom_index

import (
	"errors"
	"testing"
	"time"
)

func TestNew_PopulatesScalarFields(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, err := New(NewParams{
		AtomID:          "01970000-0000-7000-a000-000000000010",
		TenantID:        "t1",
		CourseID:        "course-1",
		Title:           "MCQ atom",
		AtomType:        "mcq",
		Difficulty:      3,
		TopicTags:       []string{"agile", "scrum"},
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		PublishedAt:     now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.AtomID == "" {
		t.Errorf("AtomID empty")
	}
	if a.TopicTags[0] != "agile" || a.TopicTags[1] != "scrum" {
		t.Errorf("TopicTags = %v", a.TopicTags)
	}
	if a.AtomType != "mcq" {
		t.Errorf("AtomType = %v", a.AtomType)
	}
	if a.CorrectOptionID != "opt-c" {
		t.Errorf("CorrectOptionID = %q; want opt-c", a.CorrectOptionID)
	}
	if a.AnswerCount != 4 {
		t.Errorf("AnswerCount = %d; want 4", a.AnswerCount)
	}
}

func TestNew_RequiresAtomIDAndTenantID(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []NewParams{
		{AtomID: "", TenantID: "t1", PublishedAt: now},
		{AtomID: "atom-1", TenantID: "", PublishedAt: now},
	}
	for _, c := range cases {
		_, err := New(c)
		if err == nil {
			t.Errorf("expected error for %+v", c)
		}
		if !errors.Is(err, ErrInvalidAtom) {
			t.Errorf("err = %v; want errors.Is(ErrInvalidAtom)", err)
		}
	}
}

func TestNew_DefaultsTopicTagsToEmpty(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, err := New(NewParams{
		AtomID:      "atom-1",
		TenantID:    "t1",
		PublishedAt: now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.TopicTags == nil {
		t.Errorf("TopicTags should default to non-nil empty slice")
	}
}

func TestIsMCQ_TrueOnlyWhenTypeMCQAndCorrectOptionIDSet(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		p    NewParams
		want bool
	}{
		{
			"mcq with correct option id",
			NewParams{AtomID: "a", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt-c", AnswerCount: 4, PublishedAt: now},
			true,
		},
		{
			"mcq without correct option id",
			NewParams{AtomID: "a", TenantID: "t1", AtomType: "mcq", PublishedAt: now},
			false,
		},
		{
			"non-mcq even with correct option id",
			NewParams{AtomID: "a", TenantID: "t1", AtomType: "essay", CorrectOptionID: "opt-c", PublishedAt: now},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := New(c.p)
			if got := a.IsMCQ(); got != c.want {
				t.Errorf("IsMCQ = %v; want %v", got, c.want)
			}
		})
	}
}

func TestGrade_ReturnsTrueOnMatchingOptionID(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, err := New(NewParams{
		AtomID:          "atom-1",
		TenantID:        "t1",
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		PublishedAt:     now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := a.Grade("opt-c")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if !got {
		t.Errorf("Grade(opt-c) = false; want true (correct = opt-c)")
	}
	got, err = a.Grade("opt-a")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if got {
		t.Errorf("Grade(opt-a) = true; want false")
	}
}

func TestGrade_EmptySelectedOptionIsFalse(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, _ := New(NewParams{
		AtomID:          "atom-1",
		TenantID:        "t1",
		AtomType:        "mcq",
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		PublishedAt:     now,
	})
	got, err := a.Grade("")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if got {
		t.Errorf("Grade(\"\") = true; want false (empty selection never correct)")
	}
}

func TestGrade_RejectsForNonMCQOrUnsetCorrectOptionID(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		p    NewParams
	}{
		{"non-mcq", NewParams{AtomID: "atom-1", TenantID: "t1", AtomType: "essay", PublishedAt: now}},
		{"mcq without answer key", NewParams{AtomID: "atom-1", TenantID: "t1", AtomType: "mcq", PublishedAt: now}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := New(c.p)
			_, err := a.Grade("opt-a")
			if err == nil {
				t.Errorf("expected error grading non-gradable atom")
			}
			if !errors.Is(err, ErrNotGradable) {
				t.Errorf("err = %v; want errors.Is(ErrNotGradable)", err)
			}
		})
	}
}

func TestSoftDelete_StampsDeletedAt(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	a, _ := New(NewParams{AtomID: "a", TenantID: "t1", PublishedAt: now})
	a.SoftDelete(now.Add(time.Hour))
	if a.DeletedAt == nil {
		t.Errorf("DeletedAt nil after SoftDelete")
	}
}

// CHO-1968 — playability projection. atom.created seeds a DRAFT (not playable)
// row; atom.published flips Status to published + sets the answer key. The
// daily dose serves only Playable() && IsAnswerable() atoms.

func TestNew_DefaultsStatusToDraft(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	a, err := New(NewParams{AtomID: "a", TenantID: "t1", PublishedAt: now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Status != StatusDraft {
		t.Errorf("Status = %q; want StatusDraft (empty must default to draft)", a.Status)
	}
	if a.Playable() {
		t.Errorf("Playable() = true; a freshly-created (draft) atom must NOT be playable")
	}
}

func TestPlayable_TrueOnlyWhenPublishedAndNotDeleted(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	published, _ := New(NewParams{AtomID: "a", TenantID: "t1", Status: StatusPublished, PublishedAt: now})
	if !published.Playable() {
		t.Errorf("published atom Playable() = false; want true")
	}

	draft, _ := New(NewParams{AtomID: "b", TenantID: "t1", Status: StatusDraft, PublishedAt: now})
	if draft.Playable() {
		t.Errorf("draft atom Playable() = true; want false")
	}

	deleted, _ := New(NewParams{AtomID: "c", TenantID: "t1", Status: StatusPublished, PublishedAt: now})
	deleted.SoftDelete(now.Add(time.Hour))
	if deleted.Playable() {
		t.Errorf("soft-deleted published atom Playable() = true; want false")
	}
}

func TestIsAnswerable_MCQ_OE_NeitherDropped(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		p    NewParams
		want bool
	}{
		{"mcq with answer key", NewParams{AtomID: "a", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt-c", AnswerCount: 4, PublishedAt: now}, true},
		{"open-ended type (essay)", NewParams{AtomID: "a", TenantID: "t1", AtomType: "essay", PublishedAt: now}, true},
		{"open-ended type (short_answer)", NewParams{AtomID: "a", TenantID: "t1", AtomType: "short_answer", PublishedAt: now}, true},
		{"mcq without answer key", NewParams{AtomID: "a", TenantID: "t1", AtomType: "mcq", PublishedAt: now}, false},
		{"empty type", NewParams{AtomID: "a", TenantID: "t1", AtomType: "", PublishedAt: now}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := New(c.p)
			if got := a.IsAnswerable(); got != c.want {
				t.Errorf("IsAnswerable() = %v; want %v", got, c.want)
			}
		})
	}
}

func TestPrimaryTopic(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"with tags returns first", []string{"agile", "scrum"}, "agile"},
		{"empty tags returns blank", []string{}, ""},
		{"nil tags returns blank", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := New(NewParams{
				AtomID:      "a",
				TenantID:    "t1",
				TopicTags:   c.tags,
				PublishedAt: now,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := a.PrimaryTopic(); got != c.want {
				t.Errorf("PrimaryTopic = %q; want %q", got, c.want)
			}
		})
	}
}

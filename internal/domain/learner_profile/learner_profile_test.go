package learner_profile

import (
	"testing"
	"time"
)

func baseFact() NewFactInput {
	return NewFactInput{
		TenantID:      "11111111-1111-1111-1111-111111111111",
		LearnerGCID:   "22222222-2222-2222-2222-222222222222",
		Type:          FactCourseCompleted,
		RefID:         "33333333-3333-3333-3333-333333333333",
		Detail:        Detail{Label: "Intro to Go"},
		SourceEventID: "44444444-4444-4444-4444-444444444444",
		OccurredAt:    time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
		Now:           time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewFact_Success(t *testing.T) {
	f, err := New(baseFact())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.ID == "" {
		t.Error("expected a generated id")
	}
	if f.Type != FactCourseCompleted {
		t.Errorf("type = %q; want course_completed", f.Type)
	}
	if f.Detail.Label != "Intro to Go" {
		t.Errorf("label = %q", f.Detail.Label)
	}
	if !f.OccurredAt.Equal(time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("occurred_at = %v", f.OccurredAt)
	}
	if !f.RecordedAt.Equal(baseFact().Now) || !f.UpdatedAt.Equal(baseFact().Now) {
		t.Errorf("recorded/updated should equal injected now; got %v / %v", f.RecordedAt, f.UpdatedAt)
	}
	if f.DeletedAt != nil {
		t.Error("new fact must be live")
	}
}

// TestNewFact_VerifiedOnly is the anti-gaming invariant (ADR-203 / L16):
// a profile fact CANNOT be constructed without a verified source event id.
func TestNewFact_VerifiedOnly(t *testing.T) {
	in := baseFact()
	in.SourceEventID = "   "
	if _, err := New(in); err == nil {
		t.Fatal("expected error: a fact must cite a verified source_event_id (no self-declaration)")
	}
}

func TestNewFact_Validation(t *testing.T) {
	cases := map[string]func(*NewFactInput){
		"no tenant":       func(in *NewFactInput) { in.TenantID = " " },
		"no learner":      func(in *NewFactInput) { in.LearnerGCID = "" },
		"bad type":        func(in *NewFactInput) { in.Type = "totally_made_up" },
		"no ref":          func(in *NewFactInput) { in.RefID = "" },
		"no source event": func(in *NewFactInput) { in.SourceEventID = "" },
		"zero occurred":   func(in *NewFactInput) { in.OccurredAt = time.Time{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseFact()
			mut(&in)
			if _, err := New(in); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

func TestNewFact_DefaultsNow(t *testing.T) {
	in := baseFact()
	in.Now = time.Time{}
	f, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.RecordedAt.IsZero() {
		t.Error("recorded_at should default to wall clock when Now is zero")
	}
}

func TestFactType_Valid(t *testing.T) {
	valid := []FactType{
		FactEnrollment, FactCourseCompleted, FactCertificationIssued,
		FactAssessmentGraded, FactPathCompleted, FactPreference,
	}
	for _, ft := range valid {
		if !ft.Valid() {
			t.Errorf("%q should be valid", ft)
		}
	}
	if FactType("nope").Valid() {
		t.Error("unknown type must be invalid")
	}
}

func TestFact_NaturalKey_StableAndScoped(t *testing.T) {
	a, _ := New(baseFact())
	b, _ := New(baseFact())
	if a.NaturalKey() != b.NaturalKey() {
		t.Error("same (tenant, learner, type, ref) must yield the same natural key (idempotent upsert)")
	}
	in := baseFact()
	in.RefID = "99999999-9999-9999-9999-999999999999"
	c, _ := New(in)
	if a.NaturalKey() == c.NaturalKey() {
		t.Error("different ref must yield a different natural key")
	}
}

func TestFact_SoftDelete(t *testing.T) {
	f, _ := New(baseFact())
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.SoftDelete(at)
	if f.DeletedAt == nil || !f.DeletedAt.Equal(at) {
		t.Error("soft delete must stamp deleted_at")
	}
	if !f.UpdatedAt.Equal(at) {
		t.Error("soft delete must advance updated_at")
	}
}

func baseActivity() NewActivityInput {
	return NewActivityInput{
		TenantID:      "11111111-1111-1111-1111-111111111111",
		LearnerGCID:   "22222222-2222-2222-2222-222222222222",
		Kind:          "completed_course",
		Summary:       "Completed Intro to Go",
		RefID:         "33333333-3333-3333-3333-333333333333",
		SourceEventID: "44444444-4444-4444-4444-444444444444",
		OccurredAt:    time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
		Now:           time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewActivity_Success(t *testing.T) {
	e, err := NewActivity(baseActivity())
	if err != nil {
		t.Fatalf("NewActivity: %v", err)
	}
	if e.ID == "" || e.Kind != "completed_course" || e.Summary == "" {
		t.Errorf("unexpected activity: %+v", e)
	}
}

func TestNewActivity_Validation(t *testing.T) {
	cases := map[string]func(*NewActivityInput){
		"no tenant":       func(in *NewActivityInput) { in.TenantID = "" },
		"no learner":      func(in *NewActivityInput) { in.LearnerGCID = "" },
		"no kind":         func(in *NewActivityInput) { in.Kind = "  " },
		"no summary":      func(in *NewActivityInput) { in.Summary = "" },
		"no source event": func(in *NewActivityInput) { in.SourceEventID = "" }, // verified-only
		"zero occurred":   func(in *NewActivityInput) { in.OccurredAt = time.Time{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseActivity()
			mut(&in)
			if _, err := NewActivity(in); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

func TestBuildView_GroupsAndSorts(t *testing.T) {
	pass := true
	score := 92.0
	mk := func(ft FactType, ref, label string, occ time.Time) *Fact {
		in := baseFact()
		in.Type = ft
		in.RefID = ref
		in.Detail = Detail{Label: label}
		in.OccurredAt = occ
		if ft == FactAssessmentGraded {
			in.Detail.Passed = &pass
			in.Detail.Score = &score
		}
		f, err := New(in)
		if err != nil {
			t.Fatalf("mk %s: %v", ft, err)
		}
		return f
	}
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	facts := []*Fact{
		mk(FactCourseCompleted, "c1", "Course One", base),
		mk(FactCourseCompleted, "c2", "Course Two", base.Add(24*time.Hour)),
		mk(FactCertificationIssued, "cert1", "Certified Gopher", base.Add(48*time.Hour)),
		mk(FactAssessmentGraded, "a1", "Quiz One", base.Add(72*time.Hour)),
		mk(FactEnrollment, "e1", "Enrolled Track", base),
	}
	acts := []*ActivityEntry{}
	for i, f := range facts {
		ae, _ := NewActivity(NewActivityInput{
			TenantID: f.TenantID, LearnerGCID: f.LearnerGCID,
			Kind: "did_" + string(f.Type), Summary: "x", RefID: f.RefID,
			SourceEventID: f.SourceEventID, OccurredAt: base.Add(time.Duration(i) * time.Hour),
			Now: base,
		})
		acts = append(acts, ae)
	}

	view := BuildView("22222222-2222-2222-2222-222222222222", facts, acts)
	if len(view.CompletedCourses) != 2 {
		t.Errorf("completed courses = %d; want 2", len(view.CompletedCourses))
	}
	if len(view.Certifications) != 1 {
		t.Errorf("certifications = %d; want 1", len(view.Certifications))
	}
	if len(view.Assessments) != 1 {
		t.Errorf("assessments = %d; want 1", len(view.Assessments))
	}
	if len(view.Assessments) == 1 && (view.Assessments[0].Passed == nil || !*view.Assessments[0].Passed) {
		t.Error("assessment pass flag should carry through")
	}
	// recent activity sorted newest-first
	if len(view.RecentActivity) < 2 || view.RecentActivity[0].OccurredAt.Before(view.RecentActivity[1].OccurredAt) {
		t.Error("recent activity must be sorted newest-first")
	}
}

func TestBuildView_SkipsDeletedFacts(t *testing.T) {
	f, _ := New(baseFact())
	f.SoftDelete(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	view := BuildView(f.LearnerGCID, []*Fact{f}, nil)
	if len(view.CompletedCourses) != 0 {
		t.Error("soft-deleted facts must not appear in the view")
	}
}

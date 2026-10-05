package weakness_upload

import (
	"testing"
	"time"
)

func validInput() NewInput {
	return NewInput{
		UploadID:      "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee",
		TenantID:      "tnt-1",
		LearnerGCID:   "gcid-1",
		UploadKind:    KindMarkedTest,
		SourceMIME:    "application/pdf",
		SourceBlobURI: "gs://chora-weakness-uploads/t/u.pdf",
		Now:           time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	}
}

func TestNew_Valid_MintsQueued(t *testing.T) {
	u, err := New(validInput())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if u.Status != StatusQueued {
		t.Errorf("status=%q want QUEUED", u.Status)
	}
	if u.UploadID == "" || u.TenantID != "tnt-1" || u.LearnerGCID != "gcid-1" {
		t.Errorf("ids = %+v", u)
	}
	if u.UploadKind != KindMarkedTest || u.SourceMIME != "application/pdf" {
		t.Errorf("kind/mime = %q/%q", u.UploadKind, u.SourceMIME)
	}
	if u.UpsertedEdgeIDs == nil {
		t.Error("UpsertedEdgeIDs must be non-nil")
	}
	if u.AnalyzedAt != nil {
		t.Error("AnalyzedAt must be nil for a fresh job")
	}
	if u.CreatedAt.IsZero() {
		t.Error("CreatedAt must be set")
	}
}

func TestNew_Rejects(t *testing.T) {
	cases := map[string]func(*NewInput){
		"missing upload_id": func(i *NewInput) { i.UploadID = "" },
		"missing tenant":    func(i *NewInput) { i.TenantID = "" },
		"missing gcid":      func(i *NewInput) { i.LearnerGCID = "" },
		"missing blob_uri":  func(i *NewInput) { i.SourceBlobURI = "" },
		"invalid kind":      func(i *NewInput) { i.UploadKind = "bogus" },
		"empty kind":        func(i *NewInput) { i.UploadKind = "" },
		"zero now":          func(i *NewInput) { i.Now = time.Time{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mut(&in)
			if _, err := New(in); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestNew_RoundTripsGoalID(t *testing.T) {
	in := validInput()
	in.GoalID = "0190aaaa-bbbb-7ccc-8ddd-000000000009"
	u, err := New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if u.GoalID != "0190aaaa-bbbb-7ccc-8ddd-000000000009" {
		t.Errorf("goal_id = %q; want round-tripped from input", u.GoalID)
	}
}

func TestNew_BlankGoalID_IsOptional(t *testing.T) {
	in := validInput() // GoalID unset — it is optional/nullable (ADR-238)
	u, err := New(in)
	if err != nil {
		t.Fatalf("New: %v (blank goal_id must not error — it is optional)", err)
	}
	if u.GoalID != "" {
		t.Errorf("goal_id = %q; want empty for an unset optional", u.GoalID)
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []string{KindMarkedTest, KindNotes, KindScribble} {
		if !ValidKind(k) {
			t.Errorf("ValidKind(%q) = false", k)
		}
	}
	for _, k := range []string{"", "bogus", "Marked_Test"} {
		if ValidKind(k) {
			t.Errorf("ValidKind(%q) = true", k)
		}
	}
}

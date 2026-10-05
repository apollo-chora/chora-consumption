// growth_edge_uploads_hint_test.go - ADR-238 D2 at the HTTP boundary.
//
// The A+ concept drawer has sent `concept_id` on every Diagnose upload since
// ADR-238 shipped, and for four days NO server-side code read it: the hint was
// accepted and silently discarded, so ranking was unbiased while both ends
// looked correct. These tests pin the field as READ, PERSISTED, and validated.
//
// They also pin goal_id/concept_id shape validation at the boundary. Both are
// cast ::uuid on insert, so without this a malformed value reaches Postgres and
// returns 22P02 → 500 - a client error wearing an outage's clothes.
package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadGrowthEdgeDoc_CarriesEntryConceptHint(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	const goalID = "0190aaaa-bbbb-7ccc-8ddd-000000000009"
	const conceptID = "0190bbbb-cccc-7ddd-8eee-000000000042"
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"goal_id":    goalID,
		"concept_id": conceptID,
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("inserted %d jobs; want 1", len(repo.inserted))
	}
	if repo.inserted[0].EntryConceptID != conceptID {
		t.Errorf("job entry_concept_id = %q; want %q - the D2 hint was read but not persisted",
			repo.inserted[0].EntryConceptID, conceptID)
	}
	if repo.inserted[0].GoalID != goalID {
		t.Errorf("job goal_id = %q; want %q", repo.inserted[0].GoalID, goalID)
	}
}

func TestUploadGrowthEdgeDoc_GoalLevelUploadStoresNoHint(t *testing.T) {
	// The goal-level "Diagnose my map" entry sends goal_id and NO concept_id, so
	// a whole-map diagnosis is never biased toward an arbitrary node (ADR-238 §5).
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"goal_id": "0190aaaa-bbbb-7ccc-8ddd-000000000009",
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if repo.inserted[0].EntryConceptID != "" {
		t.Errorf("job entry_concept_id = %q; want empty for a goal-level upload", repo.inserted[0].EntryConceptID)
	}
}

func TestUploadGrowthEdgeDoc_RejectsMalformedConceptID(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"goal_id":    "0190aaaa-bbbb-7ccc-8ddd-000000000009",
		"concept_id": "not-a-uuid",
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400 - a malformed id is the client's fault, not a 500",
			w.Code, w.Body.String())
	}
	// Rejection must happen BEFORE any side effect: no job row, no orphan blob.
	if len(repo.inserted) != 0 {
		t.Errorf("inserted %d jobs on a rejected upload; want 0", len(repo.inserted))
	}
}

func TestUploadGrowthEdgeDoc_RejectsMalformedGoalID(t *testing.T) {
	repo := &fakeUploadRepo{}
	srv := ueServer(repo, &fakeBlobUploader{})
	body, ct := multipartUploadExtra(t, "marked_test", "application/pdf", map[string]string{
		"goal_id": "12345",
	})

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, ueReq("POST", "/v1/me/growth-edges/uploads", body, ct, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s; want 400 rather than a Postgres 22P02 surfacing as 500",
			w.Code, w.Body.String())
	}
	if len(repo.inserted) != 0 {
		t.Errorf("inserted %d jobs on a rejected upload; want 0", len(repo.inserted))
	}
}

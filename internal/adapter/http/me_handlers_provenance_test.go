// me_handlers_provenance_test.go — ADR-233 D2 provenance on the /v1/me/
// learning-paths wire.
//
// ADR-233 D2 gave LearningPath a polymorphic provenance axis (source_type +
// source_id) so a path knows what it was derived from. The domain carries it and
// the pg repo hydrates it — but the HTTP projection dropped it, so every
// consumer downstream (chora-gateway's medashboard aggregator → A+) saw a
// study list as a "course" with an empty course_id.
//
// These tests assert against the MARSHALLED JSON, not the Go struct: a
// publisher and a consumer each tested against their own idea of the payload
// both stay green while the system is dead. The omitempty behaviour in
// particular only exists on the wire.
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

const (
	meCollectionID = "01970000-0000-7000-c000-000000000001"
)

// meGetPaths issues the list read and returns each item keyed by path_id.
// ListByLearner iterates a map — order is non-deterministic, so never index by
// position.
func meGetPaths(t *testing.T, srv *ExtServer) map[string]map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/me/learning-paths", nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal list body: %v", err)
	}
	items, _ := resp["items"].([]any)
	byPath := make(map[string]map[string]any, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		id, _ := m["path_id"].(string)
		byPath[id] = m
	}
	return byPath
}

// seedThreeProvenances seeds one path per source_type using the REAL domain
// constructors (each stamps its own provenance), and returns their path IDs.
func seedThreeProvenances(t *testing.T, srv *ExtServer) (courseP, collectionP, adHocP *learning_path.LearningPath) {
	t.Helper()
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()

	courseP, err := learning_path.BootstrapFromEnrollment(learning_path.BootstrapParams{
		TenantID: meTenantID, LearnerGCID: meGCID, CourseID: meCourseID,
		EnrollmentID: "enroll-1", AtomIDs: []string{meAtomID}, Title: "Scrum Cert", Now: now,
	})
	if err != nil {
		t.Fatalf("BootstrapFromEnrollment: %v", err)
	}
	collectionP, err = learning_path.NewFromCollection(learning_path.StudyListParams{
		TenantID: meTenantID, OwnerGCID: meGCID, CollectionID: meCollectionID,
		Title: "My Study List", AtomIDs: []string{meAtomID},
		StudyListEventID: "evt-1", Now: now,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	adHocP, err = learning_path.New(meTenantID, meGCID, "Hand-rolled Path", []string{meAtomID})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []*learning_path.LearningPath{courseP, collectionP, adHocP} {
		if err := srv.Paths.Save(ctx, p); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	return courseP, collectionP, adHocP
}

// TestGetMeLearningPaths_ProjectsProvenance_ADR233_D2 locks the list
// projection: every path carries its source_type, and course_id appears ONLY on
// the delivery-bound (course-sourced) path.
func TestGetMeLearningPaths_ProjectsProvenance_ADR233_D2(t *testing.T) {
	srv := newMeServer()
	courseP, collectionP, adHocP := seedThreeProvenances(t, srv)
	byPath := meGetPaths(t, srv)
	if len(byPath) != 3 {
		t.Fatalf("len items = %d; want 3", len(byPath))
	}

	// --- course-sourced: source_type "course" AND its course_id.
	course := byPath[courseP.PathID]
	if course["source_type"] != learning_path.SourceTypeCourse {
		t.Errorf("course path: source_type = %v; want %q", course["source_type"], learning_path.SourceTypeCourse)
	}
	if course["source_id"] != meCourseID {
		t.Errorf("course path: source_id = %v; want %s", course["source_id"], meCourseID)
	}
	if course["course_id"] != meCourseID {
		t.Errorf("course path: course_id = %v; want %s (delivery binding retained)", course["course_id"], meCourseID)
	}

	// --- collection-sourced (a study list): source_type "collection", and
	// course_id OMITTED from the wire entirely (it has none — omitempty).
	collection := byPath[collectionP.PathID]
	if collection["source_type"] != learning_path.SourceTypeCollection {
		t.Errorf("study list: source_type = %v; want %q", collection["source_type"], learning_path.SourceTypeCollection)
	}
	if collection["source_id"] != meCollectionID {
		t.Errorf("study list: source_id = %v; want %s", collection["source_id"], meCollectionID)
	}
	if _, present := collection["course_id"]; present {
		t.Errorf("study list: course_id key present on the wire (= %v); want OMITTED — "+
			"a study list is not a course, and an empty course_id is what made A+ render it as one",
			collection["course_id"])
	}

	// --- ad_hoc: the third real value. NOT mistakable for a study list.
	adHoc := byPath[adHocP.PathID]
	if adHoc["source_type"] != learning_path.SourceTypeAdHoc {
		t.Errorf("ad_hoc path: source_type = %v; want %q", adHoc["source_type"], learning_path.SourceTypeAdHoc)
	}
	if _, present := adHoc["source_id"]; present {
		t.Errorf("ad_hoc path: source_id key present (= %v); want OMITTED (no upstream source)", adHoc["source_id"])
	}
	if _, present := adHoc["course_id"]; present {
		t.Errorf("ad_hoc path: course_id key present (= %v); want OMITTED", adHoc["course_id"])
	}
}

// TestGetMeLearningPaths_StudyListDiscriminatorIsPositive_ADR233_D2 is the trap
// guard.
//
// source_type is NOT NULL and DEFAULTS to 'ad_hoc'; migration 0093's backfill
// only stamped 'course' on paths that already had a course_id. So every legacy
// path in prod is 'ad_hoc', and three values are live on this wire.
//
// A study list is `source_type == "collection"`. It is NEVER `!= "course"`.
// This test proves the two forms are not equivalent ON THE WIRE: the negative
// form over-selects, sweeping every legacy ad_hoc path in as a study list. It
// fails if the projection is ever changed so that non-course paths all report
// "collection" (which would make the negative form accidentally correct and
// silently mislabel legacy paths).
func TestGetMeLearningPaths_StudyListDiscriminatorIsPositive_ADR233_D2(t *testing.T) {
	srv := newMeServer()
	_, collectionP, adHocP := seedThreeProvenances(t, srv)
	byPath := meGetPaths(t, srv)

	var positive, negative []string
	for id, item := range byPath {
		st, _ := item["source_type"].(string)
		if st == learning_path.SourceTypeCollection { // the CORRECT discriminator
			positive = append(positive, id)
		}
		if st != learning_path.SourceTypeCourse { // the TRAP a consumer might write
			negative = append(negative, id)
		}
	}

	if len(positive) != 1 || positive[0] != collectionP.PathID {
		t.Errorf(`source_type == "collection" selected %v; want exactly the study list (%s)`, positive, collectionP.PathID)
	}
	if len(negative) != 2 {
		t.Fatalf(`source_type != "course" selected %d paths; want 2 — the ad_hoc path (%s) MUST be swept in, `+
			`proving the negative form is not the study-list discriminator`, len(negative), adHocP.PathID)
	}
}

// TestGetMeLearningPathByID_CarriesProvenance_ADR233_D2 — the single-path
// projection is a SECOND, independently-written copy of the same shape. It must
// carry provenance too, or a consumer that reads a study list by id still sees a
// course.
func TestGetMeLearningPathByID_CarriesProvenance_ADR233_D2(t *testing.T) {
	srv := newMeServer()
	_, collectionP, _ := seedThreeProvenances(t, srv)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths/"+collectionP.PathID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	// Assert on the raw bytes: this body is a single object, so the absence of
	// the substring proves omitempty on the wire.
	body := w.Body.String()
	if strings.Contains(body, "course_id") {
		t.Errorf("single-path body carries a course_id key for a study list: %s", body)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["source_type"] != learning_path.SourceTypeCollection {
		t.Errorf("source_type = %v; want %q (body=%s)", resp["source_type"], learning_path.SourceTypeCollection, body)
	}
	if resp["source_id"] != meCollectionID {
		t.Errorf("source_id = %v; want %s", resp["source_id"], meCollectionID)
	}
}

// TestGetMeLearningPathByID_CourseProvenance_ADR233_D2 — the course lane on the
// single-path read keeps BOTH its provenance and its delivery binding.
func TestGetMeLearningPathByID_CourseProvenance_ADR233_D2(t *testing.T) {
	srv := newMeServer()
	courseP, _, _ := seedThreeProvenances(t, srv)

	r := httptest.NewRequest("GET", "/v1/me/learning-paths/"+courseP.PathID, nil)
	r.Header.Set("X-Tenant-Id", meTenantID)
	r.Header.Set("gcid", meGCID)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["source_type"] != learning_path.SourceTypeCourse {
		t.Errorf("source_type = %v; want %q", resp["source_type"], learning_path.SourceTypeCourse)
	}
	if resp["source_id"] != meCourseID {
		t.Errorf("source_id = %v; want %s", resp["source_id"], meCourseID)
	}
	if resp["course_id"] != meCourseID {
		t.Errorf("course_id = %v; want %s (delivery binding retained alongside provenance)", resp["course_id"], meCourseID)
	}
}

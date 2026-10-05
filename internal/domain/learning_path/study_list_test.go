// study_list_test.go — RED-phase tests for ADR-233 (study-list provenance +
// the consumption/distribution boundary).
//
// Covers:
//   - D1  the aggregate carries NO audience/visibility field (reflection guard)
//   - D2  provenance is polymorphic (source_type + source_id)
//   - D3  traversal_mode is explicit; Advance() REFUSES on a spaced path
//   - D4  re-convert is an ADDITIVE re-sync (never retracts, preserves progress)
package learning_path

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testCourseID is declared in bootstrap_test.go (same package).
const (
	testCollectionID  = "01970000-0000-7000-b000-000000000001"
	testStudyListEvID = "01970000-0000-7000-c000-000000000001"
)

// -----------------------------------------------------------------------------
// D1 — boundary invariant (the load-bearing one)
// -----------------------------------------------------------------------------

// TestLearningPath_CarriesNoAudienceField_ADR233_D1 asserts by reflection that
// the aggregate never grows a content-distribution surface.
//
// ADR-233 D1: "chora_consumption models one learner's PRIVATE traversal. It MUST
// NOT carry a content-distribution surface." A `visibility` field on LearningPath
// is the *seductive* option — it makes "share my study list" a one-line change,
// and it is exactly what would make D6 (progress sharing as a chora_sharing
// projection) impossible to build cleanly later. Sharing a study list means
// sharing its SOURCE (a creation Collection / delivery Course), never the
// derived path.
func TestLearningPath_CarriesNoAudienceField_ADR233_D1(t *testing.T) {
	// Substrings that would signal a content-distribution / audience concern
	// leaking into the consumption domain.
	forbidden := []string{
		"visibility",
		"audience",
		"sharedwith",
		"shareto",
		"publicity",
		"reusevisibility",
	}

	typ := reflect.TypeOf(LearningPath{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		lower := strings.ToLower(field.Name)
		for _, bad := range forbidden {
			if strings.Contains(lower, bad) {
				t.Fatalf(
					"ADR-233 D1 VIOLATION: LearningPath.%s (%s) looks like an audience/"+
						"content-distribution field.\n"+
						"chora_consumption models ONE LEARNER'S PRIVATE TRAVERSAL and must never "+
						"carry a content-distribution surface.\n"+
						"Sharing a study list = sharing its SOURCE (creation Collection / delivery "+
						"Course), or a governed progress projection owned by chora_sharing (ADR-233 D5/D6).\n"+
						"If you believe this field is required, it needs an ADR amending ADR-233 D1 — "+
						"not a new column.",
					field.Name, field.Type,
				)
			}
		}
	}
}

// -----------------------------------------------------------------------------
// D2 — provenance is polymorphic
// -----------------------------------------------------------------------------

// TestNew_DefaultsToAdHocLinear — a hand-rolled path has no upstream source.
func TestNew_DefaultsToAdHocLinear(t *testing.T) {
	p, err := New(testTenant, testGCID, "Ad-hoc", []string{testAtom1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.SourceType != SourceTypeAdHoc {
		t.Errorf("SourceType = %q; want %q", p.SourceType, SourceTypeAdHoc)
	}
	if p.SourceID != "" {
		t.Errorf("SourceID = %q; want empty (ad_hoc has no source)", p.SourceID)
	}
	if p.TraversalMode != TraversalModeLinear {
		t.Errorf("TraversalMode = %q; want %q", p.TraversalMode, TraversalModeLinear)
	}
}

// TestBootstrapFromEnrollment_SetsCourseProvenance — the course lane keeps its
// delivery BINDING (course_id/enrollment_id) *and* gains the provenance axis.
func TestBootstrapFromEnrollment_SetsCourseProvenance(t *testing.T) {
	p, err := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     []string{testAtom1, testAtom2},
	})
	if err != nil {
		t.Fatalf("BootstrapFromEnrollment: %v", err)
	}
	if p.SourceType != SourceTypeCourse {
		t.Errorf("SourceType = %q; want %q", p.SourceType, SourceTypeCourse)
	}
	if p.SourceID != testCourseID {
		t.Errorf("SourceID = %q; want course_id %q", p.SourceID, testCourseID)
	}
	// The delivery binding is strictly MORE than provenance — it must survive.
	if p.CourseID != testCourseID {
		t.Errorf("CourseID = %q; want %q (delivery binding retained per D2)", p.CourseID, testCourseID)
	}
	if p.TraversalMode != TraversalModeLinear {
		t.Errorf("TraversalMode = %q; want %q (Straight-Up cert)", p.TraversalMode, TraversalModeLinear)
	}
}

// TestNewFromCollection_SetsCollectionProvenanceAndSpacedMode — the WS-4 lane.
func TestNewFromCollection_SetsCollectionProvenanceAndSpacedMode(t *testing.T) {
	p, err := NewFromCollection(StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		Title:            "My study list",
		AtomIDs:          []string{testAtom1, testAtom2},
		StudyListEventID: testStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	if p.SourceType != SourceTypeCollection {
		t.Errorf("SourceType = %q; want %q", p.SourceType, SourceTypeCollection)
	}
	if p.SourceID != testCollectionID {
		t.Errorf("SourceID = %q; want %q", p.SourceID, testCollectionID)
	}
	if p.TraversalMode != TraversalModeSpaced {
		t.Errorf("TraversalMode = %q; want %q (SM-2/Ebbinghaus schedules it)", p.TraversalMode, TraversalModeSpaced)
	}
	if p.StudyListEventID != testStudyListEvID {
		t.Errorf("StudyListEventID = %q; want %q (the delivery-dedupe anchor)", p.StudyListEventID, testStudyListEvID)
	}
	// A collection-derived path is NOT a delivery enrolment.
	if p.CourseID != "" || p.EnrollmentID != "" {
		t.Errorf("CourseID/EnrollmentID = %q/%q; want empty (no delivery binding)", p.CourseID, p.EnrollmentID)
	}
	if p.CurrentIndex != 0 {
		t.Errorf("CurrentIndex = %d; want 0 (inert for spaced)", p.CurrentIndex)
	}
}

func TestNewFromCollection_RejectsMissingRequiredFields(t *testing.T) {
	base := StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		AtomIDs:          []string{testAtom1},
		StudyListEventID: testStudyListEvID,
	}
	cases := map[string]func(*StudyListParams){
		"no tenant":     func(p *StudyListParams) { p.TenantID = "" },
		"no owner":      func(p *StudyListParams) { p.OwnerGCID = "" },
		"no collection": func(p *StudyListParams) { p.CollectionID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := base
			mutate(&p)
			if _, err := NewFromCollection(p); err == nil {
				t.Fatalf("NewFromCollection(%s) = nil error; want a refusal", name)
			}
		})
	}
}

// TestNewFromCollection_DefaultsTitle — a blank title still yields a usable path.
func TestNewFromCollection_DefaultsTitle(t *testing.T) {
	p, err := NewFromCollection(StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		AtomIDs:          []string{testAtom1},
		StudyListEventID: testStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	if strings.TrimSpace(p.Title) == "" {
		t.Error("Title is blank; want a non-empty default")
	}
}

// TestNewFromCollection_AcceptsEmptyAtomList — content tolerance, mirroring
// BootstrapFromEnrollment's OPEN-1: an atom may be excluded at convert time
// (ADR-233 D11) or arrive later; a 0-atom derived path is still a real path.
// (Zero ENTITLED atoms is refused UPSTREAM in chora-creation with 409, never here.)
func TestNewFromCollection_AcceptsEmptyAtomList(t *testing.T) {
	p, err := NewFromCollection(StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		StudyListEventID: testStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection with 0 atoms: %v", err)
	}
	if len(p.AtomIDs) != 0 {
		t.Errorf("AtomIDs = %v; want empty", p.AtomIDs)
	}
}

// -----------------------------------------------------------------------------
// D3 — traversal mode is explicit; the cursor is INERT for spaced paths
// -----------------------------------------------------------------------------

// TestAdvance_RefusesOnSpacedTraversal — ADR-233 D3.
//
// `current_index` is a LINEAR cursor. A spaced-repetition study list is
// scheduled by decay/due off sm2_states, never by a cursor. Leaving the cursor
// live for spaced paths is exactly the silent-nonsense the ADR forbids — so
// Advance() must REFUSE rather than quietly move a meaningless integer.
func TestAdvance_RefusesOnSpacedTraversal(t *testing.T) {
	p, err := NewFromCollection(StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		AtomIDs:          []string{testAtom1, testAtom2},
		StudyListEventID: testStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}

	res, err := p.Advance(testAtom1, time.Now().UTC())
	if !errors.Is(err, ErrSpacedPathNoCursor) {
		t.Fatalf("Advance on spaced path: err = %v; want ErrSpacedPathNoCursor (ADR-233 D3)", err)
	}
	if res.Advanced || res.Completed {
		t.Errorf("Advance on spaced path returned %+v; want zero-value (cursor is inert)", res)
	}
	if p.CurrentIndex != 0 {
		t.Errorf("CurrentIndex moved to %d on a spaced path; the cursor must stay inert", p.CurrentIndex)
	}
	if p.CompletedAt != nil {
		t.Error("CompletedAt stamped on a spaced path; completion is not cursor-derived here")
	}
}

// TestAdvance_StillAdvancesLinearPaths — regression guard: the D3 refusal must
// not break the Straight-Up cert lane.
func TestAdvance_StillAdvancesLinearPaths(t *testing.T) {
	p, err := BootstrapFromEnrollment(BootstrapParams{
		TenantID:    testTenant,
		LearnerGCID: testGCID,
		CourseID:    testCourseID,
		AtomIDs:     []string{testAtom1, testAtom2},
	})
	if err != nil {
		t.Fatalf("BootstrapFromEnrollment: %v", err)
	}
	res, err := p.Advance(testAtom1, time.Now().UTC())
	if err != nil {
		t.Fatalf("Advance on linear path: %v", err)
	}
	if !res.Advanced {
		t.Error("linear path did not advance")
	}
	if p.CurrentIndex != 1 {
		t.Errorf("CurrentIndex = %d; want 1", p.CurrentIndex)
	}
}

// -----------------------------------------------------------------------------
// D4 — derivation is additive and learner-controlled
// -----------------------------------------------------------------------------

// TestReConvert_IsAdditiveReSync_ADR233_D4 — "A source may NEVER retract atoms
// from a derived path."
//
// Re-converting a collection whose atoms have CHANGED (one removed upstream, one
// added) must: keep the removed atom (the learner may have progress in it),
// append the new one, and preserve the existing cursor.
func TestReConvert_IsAdditiveReSync_ADR233_D4(t *testing.T) {
	p, err := NewFromCollection(StudyListParams{
		TenantID:         testTenant,
		OwnerGCID:        testGCID,
		CollectionID:     testCollectionID,
		AtomIDs:          []string{testAtom1, testAtom2},
		StudyListEventID: testStudyListEvID,
	})
	if err != nil {
		t.Fatalf("NewFromCollection: %v", err)
	}
	// The learner has progress (a spaced path's real progress lives in sm2_states,
	// but CurrentIndex must still survive an additive re-sync untouched).
	p.CurrentIndex = 1
	before := p.CurrentIndex

	// Upstream collection now = {atom2, atom3}: atom1 was REMOVED, atom3 ADDED.
	now := time.Now().UTC()
	for _, a := range []string{testAtom2, testAtom3} {
		p.AppendAtom(a, now)
	}

	// atom1 must NOT be retracted.
	if !containsAtom(p.AtomIDs, testAtom1) {
		t.Errorf("atom1 was RETRACTED on re-sync; ADR-233 D4: a source may NEVER retract "+
			"atoms from a derived path (the learner may have progress in it). AtomIDs=%v", p.AtomIDs)
	}
	// atom3 must be appended.
	if !containsAtom(p.AtomIDs, testAtom3) {
		t.Errorf("atom3 was not appended on re-sync; AtomIDs=%v", p.AtomIDs)
	}
	// atom2 must not be duplicated (AppendAtom is idempotent).
	if n := countAtom(p.AtomIDs, testAtom2); n != 1 {
		t.Errorf("atom2 appears %d times; AppendAtom must be idempotent", n)
	}
	// Existing progress is preserved.
	if p.CurrentIndex != before {
		t.Errorf("CurrentIndex = %d; want %d preserved across an additive re-sync", p.CurrentIndex, before)
	}
}

func containsAtom(list []string, want string) bool {
	for _, a := range list {
		if a == want {
			return true
		}
	}
	return false
}

func countAtom(list []string, want string) int {
	n := 0
	for _, a := range list {
		if a == want {
			n++
		}
	}
	return n
}

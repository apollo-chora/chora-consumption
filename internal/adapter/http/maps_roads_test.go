// maps_roads_test.go: C4 (UX Track U, plan section 8) RED tests for the Roads
// layer on the map read:
//
//	GET /v1/me/maps/{goalId}/graph  → each concept carries roads[]
//
// A road is the in-domain intersection of a COURSE-BOUND LearningPath's AtomIDs
// with a ConceptNode's AtomRefs. Both live in chora_consumption, so the drawer's
// "also on the certificate path" line costs no cross-DB read and no new
// projection. The course TITLE comes from the course_directory projection
// (migration 0072), which is also in this database.
//
// Four things these tests pin, each of which is a way the layer could lie:
//
//   - An ad-hoc or collection-derived path is NOT a road. Roads name the
//     course-bound path, and a study list the learner built is not a certificate.
//   - A course with no course_directory row carries its courseId and NO title.
//     The projection is fed over Pub/Sub and can legitimately be behind, and a
//     fabricated title is worse than an honest id the SPA can resolve itself.
//   - The path's own label is never promoted to a course title when it is the
//     bootstrap placeholder. learning_path/bootstrap.go defaults Title to the
//     literal "Course " + CourseID, which looks like a title and is not one.
//   - A failed paths read omits roads and SAYS SO (roadsPartial), because a
//     silent empty roads[] would tell the learner this concept is on no
//     certificate path, which is a confident false claim.
//
// White-box (package http), reuses fmStubConcepts / reStubEdges / fmTenantID /
// fmGCID / mapsGoalStub / mapsServe from the sibling map tests.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	repoinmem "github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	coursedir "github.com/apollo-chora/chora-consumption/internal/domain/course_directory"
	conceptgraph "github.com/apollo-chora/chora-consumption/internal/domain/concept_graph"
	"github.com/apollo-chora/chora-consumption/internal/domain/goal"
	"github.com/apollo-chora/chora-consumption/internal/domain/learning_path"
)

// ---- doubles ----------------------------------------------------------------

// roadsPathRepo is the real in-memory path repo with one seam: ListByLearner can
// be made to fail, so the degraded case is exercised through the SAME port the
// handler calls rather than a hand-rolled stub of every method.
type roadsPathRepo struct {
	learning_path.Repo
	listErr error
}

func newRoadsPathRepo() *roadsPathRepo {
	return &roadsPathRepo{Repo: repoinmem.NewLearningPathRepo()}
}

func (r *roadsPathRepo) ListByLearner(ctx context.Context, tenantID, gcid string, limit int) ([]*learning_path.LearningPath, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.Repo.ListByLearner(ctx, tenantID, gcid, limit)
}

// ---- fixture ----------------------------------------------------------------

const (
	roadsAtomLin1  = "01920000-0000-7000-8000-00000000a001"
	roadsAtomLin2  = "01920000-0000-7000-8000-00000000a002"
	roadsAtomQuad1 = "01920000-0000-7000-8000-00000000a003"
	roadsAtomOff   = "01920000-0000-7000-8000-00000000a004"
	roadsCourseID  = "01920000-0000-7000-8000-00000000c001"
	roadsCourse2ID = "01920000-0000-7000-8000-00000000c002"
)

// roadsConcepts: the Algebra tree, with atom refs on two of the three nodes.
// c-quad deliberately carries an atom that sits on NO path (the negative
// control), and c-root carries none at all.
func roadsConcepts() []*conceptgraph.ConceptNode {
	return []*conceptgraph.ConceptNode{
		{ConceptID: "c-root", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Algebra"},
		{ConceptID: "c-lin", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Linear Equations",
			AtomRefs: []string{roadsAtomLin1, roadsAtomLin2}},
		{ConceptID: "c-quad", TenantID: fmTenantID, LearnerGCID: fmGCID, Title: "Quadratics",
			AtomRefs: []string{roadsAtomOff}},
	}
}

// roadsSeedCoursePath stores a course-bound path whose atom list puts the
// concept's atoms at a known position.
func roadsSeedCoursePath(t *testing.T, repo *roadsPathRepo, pathID, courseID, title string, atomIDs []string) {
	t.Helper()
	p := &learning_path.LearningPath{
		PathID: pathID, TenantID: fmTenantID, OwnerGCID: fmGCID,
		Title: title, AtomIDs: atomIDs, CourseID: courseID,
		SourceType: learning_path.SourceTypeCourse, SourceID: courseID,
		TraversalMode: learning_path.TraversalModeLinear,
	}
	if err := repo.Repo.Save(context.Background(), p); err != nil {
		t.Fatalf("seed course path: %v", err)
	}
}

// roadsSeedAdHocPath stores a path with NO course binding: a study list the
// learner built. It must never surface as a road.
func roadsSeedAdHocPath(t *testing.T, repo *roadsPathRepo, pathID, title string, atomIDs []string) {
	t.Helper()
	p := &learning_path.LearningPath{
		PathID: pathID, TenantID: fmTenantID, OwnerGCID: fmGCID,
		Title: title, AtomIDs: atomIDs,
		SourceType: learning_path.SourceTypeAdHoc, TraversalMode: learning_path.TraversalModeLinear,
	}
	if err := repo.Repo.Save(context.Background(), p); err != nil {
		t.Fatalf("seed ad-hoc path: %v", err)
	}
}

// roadsServer wires the map read plus the paths repo and the course directory.
// A nil directory means the projection is unwired, which must read the same as
// a projection with no row: an id and no title.
func roadsServer(paths learning_path.Repo, dir CourseDirectoryReader) *ExtServer {
	g := mapsGoal("g-1", mapsPtr("c-root"))
	return &ExtServer{
		Goals:           &mapsGoalStub{list: []*goal.Goal{g}, byID: map[string]*goal.Goal{"g-1": g}},
		Concepts:        &fmStubConcepts{out: roadsConcepts()},
		ConceptEdges:    &reStubEdges{out: mapsFixtureEdges()},
		Paths:           paths,
		CourseDirectory: dir,
	}
}

// roadsDirectory returns an in-memory course directory holding the given
// course_id → title rows.
func roadsDirectory(t *testing.T, titles map[string]string) *coursedir.InMemCourseDirectory {
	t.Helper()
	d := coursedir.NewInMemCourseDirectory()
	for id, title := range titles {
		if err := d.Upsert(context.Background(), coursedir.CourseDirectoryEntry{
			CourseID: id, Title: title, UpdatedAt: mapsGoal("x", nil).CreatedAt,
		}); err != nil {
			t.Fatalf("seed directory: %v", err)
		}
	}
	return d
}

// ---- wire shape -------------------------------------------------------------

type roadDTOWire struct {
	PathID       string `json:"pathId"`
	CourseID     string `json:"courseId"`
	CourseTitle  string `json:"courseTitle"`
	PathLabel    string `json:"pathLabel"`
	AtomPosition int    `json:"atomPosition"`
	AtomCount    int    `json:"atomCount"`
}

type roadsGraphWire struct {
	Concepts []struct {
		ConceptID string        `json:"conceptId"`
		Roads     []roadDTOWire `json:"roads"`
	} `json:"concepts"`
	RoadsPartial bool `json:"roadsPartial"`
}

func roadsGet(t *testing.T, srv *ExtServer) roadsGraphWire {
	t.Helper()
	w := mapsServe(srv, http.MethodGet, "/v1/me/maps/g-1/graph", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var out roadsGraphWire
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return out
}

func roadsFor(t *testing.T, g roadsGraphWire, conceptID string) []roadDTOWire {
	t.Helper()
	for _, c := range g.Concepts {
		if c.ConceptID == conceptID {
			return c.Roads
		}
	}
	t.Fatalf("concept %s absent from graph", conceptID)
	return nil
}

// ---- tests ------------------------------------------------------------------

// The seam itself: a concept whose atoms sit on a course-bound path carries a
// road naming that path, its course, the resolved course title, and WHERE on
// the road the first of those atoms sits (the drawer's "module N").
func TestMapsRoads_ConceptCarriesTheCourseBoundPathThatTouchesIt(t *testing.T) {
	paths := newRoadsPathRepo()
	// Linear's two atoms sit at positions 2 and 3 of a four-atom course path.
	roadsSeedCoursePath(t, paths, "p-cert", roadsCourseID, "Algebra I",
		[]string{"01920000-0000-7000-8000-00000000a0ff", roadsAtomLin1, roadsAtomLin2, roadsAtomQuad1})
	srv := roadsServer(paths, roadsDirectory(t, map[string]string{roadsCourseID: "Algebra I"}))

	got := roadsGet(t, srv)
	roads := roadsFor(t, got, "c-lin")
	if len(roads) != 1 {
		t.Fatalf("c-lin roads = %d; want 1 (%+v)", len(roads), roads)
	}
	r := roads[0]
	if r.PathID != "p-cert" {
		t.Errorf("pathId = %q; want p-cert", r.PathID)
	}
	if r.CourseID != roadsCourseID {
		t.Errorf("courseId = %q; want %q", r.CourseID, roadsCourseID)
	}
	if r.CourseTitle != "Algebra I" {
		t.Errorf("courseTitle = %q; want %q (course_directory row)", r.CourseTitle, "Algebra I")
	}
	if r.AtomPosition != 2 {
		t.Errorf("atomPosition = %d; want 2 (1-based index of the FIRST overlapping atom)", r.AtomPosition)
	}
	if r.AtomCount != 2 {
		t.Errorf("atomCount = %d; want 2 (both of this concept's atoms are on the road)", r.AtomCount)
	}
	if got.RoadsPartial {
		t.Errorf("roadsPartial = true on a clean read; want false")
	}
}

// The negative control: a concept whose atoms sit on no path carries no roads,
// and a concept with no atom refs at all carries none either. Without this a
// layer that painted every concept would pass the test above.
func TestMapsRoads_ConceptOffEveryRoadCarriesNone(t *testing.T) {
	paths := newRoadsPathRepo()
	roadsSeedCoursePath(t, paths, "p-cert", roadsCourseID, "Algebra I",
		[]string{roadsAtomLin1, roadsAtomLin2})
	srv := roadsServer(paths, roadsDirectory(t, map[string]string{roadsCourseID: "Algebra I"}))

	got := roadsGet(t, srv)
	if roads := roadsFor(t, got, "c-quad"); len(roads) != 0 {
		t.Errorf("c-quad roads = %+v; want none (its atom is on no path)", roads)
	}
	if roads := roadsFor(t, got, "c-root"); len(roads) != 0 {
		t.Errorf("c-root roads = %+v; want none (it has no atom refs)", roads)
	}
}

// A study list the learner assembled is not a certificate. Only a COURSE-BOUND
// path is a road, so an ad-hoc path sharing the same atoms contributes nothing.
func TestMapsRoads_AnAdHocPathIsNotARoad(t *testing.T) {
	paths := newRoadsPathRepo()
	roadsSeedAdHocPath(t, paths, "p-mine", "My revision list", []string{roadsAtomLin1, roadsAtomLin2})
	srv := roadsServer(paths, roadsDirectory(t, nil))

	got := roadsGet(t, srv)
	if roads := roadsFor(t, got, "c-lin"); len(roads) != 0 {
		t.Errorf("c-lin roads = %+v; want none (an ad-hoc path is not a road)", roads)
	}
}

// The course_directory projection is fed over Pub/Sub and can be behind. No row
// means the road carries its courseId and NO title, so the SPA resolves the name
// from the courses read it already has. It must never invent one.
func TestMapsRoads_NoDirectoryRowCarriesTheIdAndNoTitle(t *testing.T) {
	paths := newRoadsPathRepo()
	roadsSeedCoursePath(t, paths, "p-cert", roadsCourseID, "Algebra I", []string{roadsAtomLin1})
	srv := roadsServer(paths, roadsDirectory(t, nil)) // directory wired, no rows

	got := roadsGet(t, srv)
	roads := roadsFor(t, got, "c-lin")
	if len(roads) != 1 {
		t.Fatalf("c-lin roads = %d; want 1", len(roads))
	}
	if roads[0].CourseID != roadsCourseID {
		t.Errorf("courseId = %q; want %q (always carried)", roads[0].CourseID, roadsCourseID)
	}
	if roads[0].CourseTitle != "" {
		t.Errorf("courseTitle = %q; want empty (no directory row: never fabricate a name)", roads[0].CourseTitle)
	}
}

// learning_path/bootstrap.go defaults an enrolment with no title to the literal
// "Course " + CourseID. That string looks like a course name and is not one, so
// it must never reach the wire as pathLabel; a real label must.
func TestMapsRoads_TheBootstrapPlaceholderIsNeverServedAsALabel(t *testing.T) {
	paths := newRoadsPathRepo()
	roadsSeedCoursePath(t, paths, "p-placeholder", roadsCourseID, "Course "+roadsCourseID,
		[]string{roadsAtomLin1})
	roadsSeedCoursePath(t, paths, "p-named", roadsCourse2ID, "Algebra, term 2",
		[]string{roadsAtomLin2})
	srv := roadsServer(paths, roadsDirectory(t, nil))

	got := roadsGet(t, srv)
	roads := roadsFor(t, got, "c-lin")
	if len(roads) != 2 {
		t.Fatalf("c-lin roads = %d; want 2", len(roads))
	}
	byPath := map[string]roadDTOWire{}
	for _, r := range roads {
		byPath[r.PathID] = r
	}
	if lbl := byPath["p-placeholder"].PathLabel; lbl != "" {
		t.Errorf("pathLabel = %q; want empty (the bootstrap placeholder is not a label)", lbl)
	}
	if lbl := byPath["p-named"].PathLabel; lbl != "Algebra, term 2" {
		t.Errorf("pathLabel = %q; want %q (a real label is carried)", lbl, "Algebra, term 2")
	}
}

// A failed paths read omits the layer and SAYS SO. A silent empty roads[] would
// tell the learner this concept is on no certificate path, which is a confident
// false claim built from a read that never happened. The map itself still
// renders: the overlay is additive and fail-soft, never a 5xx.
func TestMapsRoads_AFailedPathsReadIsPartialNotEmpty(t *testing.T) {
	paths := newRoadsPathRepo()
	roadsSeedCoursePath(t, paths, "p-cert", roadsCourseID, "Algebra I", []string{roadsAtomLin1})
	paths.listErr = errors.New("paths repo down")
	srv := roadsServer(paths, roadsDirectory(t, map[string]string{roadsCourseID: "Algebra I"}))

	got := roadsGet(t, srv)
	if !got.RoadsPartial {
		t.Errorf("roadsPartial = false after a failed paths read; want true (empty must not read as a fact)")
	}
	if roads := roadsFor(t, got, "c-lin"); len(roads) != 0 {
		t.Errorf("c-lin roads = %+v; want none when the read failed", roads)
	}
}

// An unwired paths repo is the same honest answer as a failed read: the layer
// cannot be computed, so it is partial rather than an empty fact.
func TestMapsRoads_AnUnwiredPathsRepoIsPartial(t *testing.T) {
	srv := roadsServer(nil, roadsDirectory(t, nil))

	got := roadsGet(t, srv)
	if !got.RoadsPartial {
		t.Errorf("roadsPartial = false with no paths repo wired; want true")
	}
}

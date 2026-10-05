// enrollment_curriculum_test.go — CHO-2169, RED-first.
//
// 🔴 A LearningPath bootstrapped from an enrolment came out EMPTY, for EVERY
// course in production. A+ showed the learner "0 atoms" and an empty Atoms
// panel while the Course Curriculum right below it listed all five items.
//
// THE TWO LINKAGES. "Which atoms are in this course?" was answered in two
// different places that never met:
//
//	R+ COMPOSES a curriculum in chora_delivery  → course_content_items
//	                                            → chora.delivery.course.content_composed.v1
//	                                            → consumption's course_content projection ✅ (live, correct)
//
//	the BOOTSTRAP READ  atom_index.course_id    ← chora.creation.atom.created.v1
//	                                            ← learning_atoms.course_id  ❌ (an atom
//	                                               self-declaring its course — which R+
//	                                               curriculum composition NEVER writes)
//
// So `atom_index.course_id` was NULL for 122 of 128 live atoms, ListByCourse
// returned nothing, and every course-bootstrapped path in prod held 0 atoms.
// The curriculum was sitting in consumption the whole time, correctly projected
// from delivery, and nothing read it.
//
// 🔴 WHY THE OLD TESTS WERE GREEN. They seeded atom_index *with a course_id*
// (`seedAtomCreated(t, atomIdx, id, "t1", "c1")`) — hand-feeding the subscriber
// the very linkage production cannot produce. A fake that supplies what prod
// does not is the same blind spot that hid CHO-2153's RLS bug and CHO-2173's.
// These tests therefore drive the subscriber through the COURSE-CONTENT
// projection, which is the only thing prod actually fills.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/course_content"
)

// seedCurriculum plants a curriculum exactly as chora-delivery composes one:
// a MIXED set of kinds, in the instructor's positional order.
func seedCurriculum(t *testing.T, repo course_content.ProjectionRepo, tenantID, courseID string, items []*course_content.Item) {
	t.Helper()
	if err := repo.ReplaceByCourse(context.Background(), tenantID, courseID, items); err != nil {
		t.Fatalf("seed course_content: %v", err)
	}
}

func cItem(pos int, kind course_content.Kind, ref, title string) *course_content.Item {
	return &course_content.Item{
		ItemID: "item-" + ref, TenantID: "t1", CourseID: "c1",
		Kind: kind, Ref: ref, Title: title, Position: pos,
	}
}

// seedCourseAtoms plants an all-atom curriculum for (tenant, course) — the
// PRODUCTION linkage.
//
// It replaces the old `seedAtomCreated(t, atomIdx, id, tenant, course)` pattern,
// which seeded atom_index WITH a course_id. That fake handed the subscriber the
// one thing production cannot produce: R+ composes a curriculum in chora_delivery
// and never writes a course tag back onto the atom in chora_creation. The tests
// were green; the feature was dead. Seed the curriculum, not the atom.
func seedCourseAtoms(t *testing.T, repo course_content.ProjectionRepo, tenantID, courseID string, atomIDs ...string) {
	t.Helper()
	items := make([]*course_content.Item, 0, len(atomIDs))
	for i, id := range atomIDs {
		items = append(items, &course_content.Item{
			ItemID: "item-" + id, TenantID: tenantID, CourseID: courseID,
			Kind: course_content.KindAtom, Ref: id, Title: id, Position: i,
		})
	}
	if err := repo.ReplaceByCourse(context.Background(), tenantID, courseID, items); err != nil {
		t.Fatalf("seed curriculum: %v", err)
	}
}

func enrolPayload() EnrollmentCreatedPayload {
	return EnrollmentCreatedPayload{
		EnrollmentID: "enr-1",
		CourseID:     "c1",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		EnrolledAt:   time.Now().UTC(),
	}
}

func enrolEnvelope() events.Envelope {
	env := newTestEnvelope("evt-curriculum", "t1", "phyllis")
	env.SourceService = "chora-delivery"
	return env
}

// =============================================================================
// The bootstrap sources its atoms from the CURRICULUM
// =============================================================================

// The headline. The curriculum is the only place that knows which atoms belong
// to a course, and it is already projected into consumption.
//
// Note the ordering trap baked into the fixture: the atoms are named so that a
// LEXICOGRAPHIC sort (what the old code did — "sort by AtomID, a safe MVP
// order") yields ["a-alpha", "a-zulu"], while the INSTRUCTOR's order is
// ["a-zulu", "a-alpha"]. A path that merely happens to be non-empty is not
// enough: it must be in the order the instructor taught.
func TestEnrollmentCreated_SourcesAtomsFromTheCurriculumInInstructorOrder(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)

	seedCurriculum(t, content, "t1", "c1", []*course_content.Item{
		cItem(0, course_content.KindVideo, "gs://lecture.mp4", "lecture"),
		cItem(1, course_content.KindAtom, "a-zulu", "Taught first"),
		cItem(2, course_content.KindAssessment, "as-1", "graded exam"),
		cItem(3, course_content.KindAtom, "a-alpha", "Taught second"),
		cItem(4, course_content.KindLiveClassroom, "lc-1", "live session"),
	})

	if err := sub.Handle(context.Background(), enrolEnvelope(), enrolPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v", err)
	}
	if len(p.AtomIDs) != 2 {
		t.Fatalf("AtomIDs = %v (len %d); want the 2 curriculum atoms — a 0-atom path is the "+
			"CHO-2169 defect, and it is what EVERY course-bootstrapped path in prod looked like",
			p.AtomIDs, len(p.AtomIDs))
	}
	if p.AtomIDs[0] != "a-zulu" || p.AtomIDs[1] != "a-alpha" {
		t.Errorf("AtomIDs = %v; want [a-zulu a-alpha] — the INSTRUCTOR's curriculum order. "+
			"[a-alpha a-zulu] means the atoms were re-sorted lexicographically, which is the "+
			"old sort-by-AtomID hack the curriculum's `position` exists to replace", p.AtomIDs)
	}
}

// Only `atom` items are playable. A video, a PDF, a graded assessment and a
// live classroom are all real curriculum items — and none of them is a
// LearningAtom. Letting their refs into AtomIDs would put a GCS object path
// where an atom_id belongs.
func TestEnrollmentCreated_NonAtomCurriculumItemsNeverEnterThePath(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, events.NewInMemoryPublisher())

	seedCurriculum(t, content, "t1", "c1", []*course_content.Item{
		cItem(0, course_content.KindVideo, "gs://bucket/lecture.mp4", "lecture"),
		cItem(1, course_content.KindDocument, "gs://bucket/handbook.pdf", "handbook"),
		cItem(2, course_content.KindAssessment, "as-1", "exam"),
		cItem(3, course_content.KindLiveClassroom, "lc-1", "live"),
		cItem(4, course_content.KindAtom, "a-only", "the one real atom"),
	})

	if err := sub.Handle(context.Background(), enrolEnvelope(), enrolPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v", err)
	}
	if len(p.AtomIDs) != 1 || p.AtomIDs[0] != "a-only" {
		t.Errorf("AtomIDs = %v; want [a-only] — only kind=atom is playable", p.AtomIDs)
	}
}

// The OPEN-1 tolerance survives: a published course with no atoms YET still
// bootstraps a 0-atom path, so an enrolled learner is never shown "not
// enrolled". The difference is that this is now a real emptiness, not an
// artefact of reading the wrong projection.
func TestEnrollmentCreated_CourseWithNoAtomsStillBootstrapsAnEmptyPath(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, events.NewInMemoryPublisher())

	seedCurriculum(t, content, "t1", "c1", []*course_content.Item{
		cItem(0, course_content.KindVideo, "gs://bucket/lecture.mp4", "lecture"),
	})

	if err := sub.Handle(context.Background(), enrolEnvelope(), enrolPayload()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if err != nil {
		t.Fatalf("the learner must still have a path (OPEN-1 tolerance): %v", err)
	}
	if len(p.AtomIDs) != 0 {
		t.Errorf("AtomIDs = %v; want empty", p.AtomIDs)
	}
}

// =============================================================================
// Retroactive append — on the lane where atoms ACTUALLY join a course
// =============================================================================

// An instructor adds an atom to a course AFTER learners have enrolled.
//
// Today the retroactive append (R2) hangs off `atom.created.v1` — an atom being
// AUTHORED. But an atom joins a COURSE by being composed into its curriculum,
// which is a chora-delivery act and fires `course.content_composed.v1`. Authoring
// an atom in A+ does not put it in anyone's course, and composing an existing
// atom into a course does not re-author it. So R2 hung off an event that can
// never fire for an R+-composed course, and the append could never happen.
//
// The back-fill therefore belongs on the content_composed lane.
func TestCourseContentComposed_BackfillsAtomsIntoAlreadyEnrolledPaths(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()

	// A learner enrols while the course has ONE atom.
	enrolSub := NewEnrollmentCreatedSubscriber(pathRepo, content, events.NewInMemoryPublisher())
	seedCurriculum(t, content, "t1", "c1", []*course_content.Item{
		cItem(0, course_content.KindAtom, "a-first", "First"),
	})
	if err := enrolSub.Handle(context.Background(), enrolEnvelope(), enrolPayload()); err != nil {
		t.Fatalf("enrol Handle: %v", err)
	}

	// The instructor then composes a SECOND atom into the curriculum.
	contentSub := NewCourseContentComposedSubscriber(content, pathRepo)
	env := newTestEnvelope("evt-compose-2", "t1", "instructor")
	env.SourceService = "chora-delivery"
	err := contentSub.Handle(env, CourseContentComposedPayload{
		CourseID: "c1",
		Items: []CourseContentItemPayload{
			{ItemID: "item-a-first", Kind: "atom", Ref: "a-first", Title: "First", Position: 0},
			{ItemID: "item-a-second", Kind: "atom", Ref: "a-second", Title: "Second", Position: 1},
		},
	})
	if err != nil {
		t.Fatalf("content_composed Handle: %v", err)
	}

	p, gerr := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if gerr != nil {
		t.Fatalf("GetByCourseAndGCID: %v", gerr)
	}
	if len(p.AtomIDs) != 2 || p.AtomIDs[1] != "a-second" {
		t.Errorf("AtomIDs = %v; want [a-first a-second] — an atom composed into the course "+
			"after enrolment must reach the already-enrolled learner's path", p.AtomIDs)
	}
}

// D4 (ADR-233): a source may never RETRACT atoms from a derived path. If an
// instructor removes an atom from the curriculum, the learner keeps it — they
// may already have progress in it. The back-fill is strictly additive.
func TestCourseContentComposed_NeverRetractsAnAtomFromALearnersPath(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()

	enrolSub := NewEnrollmentCreatedSubscriber(pathRepo, content, events.NewInMemoryPublisher())
	seedCurriculum(t, content, "t1", "c1", []*course_content.Item{
		cItem(0, course_content.KindAtom, "a-keep", "Keep"),
		cItem(1, course_content.KindAtom, "a-dropped", "Dropped later"),
	})
	if err := enrolSub.Handle(context.Background(), enrolEnvelope(), enrolPayload()); err != nil {
		t.Fatalf("enrol Handle: %v", err)
	}

	// The instructor removes a-dropped from the curriculum.
	contentSub := NewCourseContentComposedSubscriber(content, pathRepo)
	env := newTestEnvelope("evt-compose-shrunk", "t1", "instructor")
	env.SourceService = "chora-delivery"
	if err := contentSub.Handle(env, CourseContentComposedPayload{
		CourseID: "c1",
		Items: []CourseContentItemPayload{
			{ItemID: "item-a-keep", Kind: "atom", Ref: "a-keep", Title: "Keep", Position: 0},
		},
	}); err != nil {
		t.Fatalf("content_composed Handle: %v", err)
	}

	p, gerr := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if gerr != nil {
		t.Fatalf("GetByCourseAndGCID: %v", gerr)
	}
	if len(p.AtomIDs) != 2 {
		t.Errorf("AtomIDs = %v; want BOTH still present. A source may never retract an atom "+
			"from a derived path (ADR-233 D4) — the learner may have progress in it", p.AtomIDs)
	}
}

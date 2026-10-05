// Package subscribers — RED phase tests for the cross-domain event
// subscribers wired in chora-consumption:
//
//   - chora.creation.atom.created.v1   → atom_index projection upsert
//   - chora.delivery.enrollment.created.v1 → LearningPath bootstrap
//
// Per ddd-enforcement HARD RULE: chora-consumption never queries
// chora_creation OR chora_delivery directly. Subscribers populate local
// projections to enable atom-centric reads + LearningPath advancement.
//
// Tests cover envelope validation, idempotency on duplicate event_id,
// trace-context propagation, and the projection's correctness.
package subscribers

import (
	"context"
	"testing"
	"time"

	"github.com/apollo-chora/chora-consumption/internal/adapter/events"
	"github.com/apollo-chora/chora-consumption/internal/adapter/repo/inmem"
	"github.com/apollo-chora/chora-consumption/internal/domain/atom_index"
)

func newTestEnvelope(eventID, tenantID, gcid string) events.Envelope {
	now := time.Now().UTC()
	if eventID == "" {
		eventID = "01970000-0000-7000-d000-000000000001"
	}
	return events.Envelope{
		EventID:        eventID,
		IdempotencyKey: eventID,
		TenantID:       tenantID,
		GCID:           gcid,
		OccurredAt:     now,
		PublishedAt:    now,
		Traceparent:    "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-00",
		SourceProject:  "chora-content",
		SourceService:  "chora-creation",
		SchemaVersion:  1,
	}
}

// ---- AtomCreated subscriber ----

func TestAtomCreatedSubscriber_HydratesAtomIndex(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, inmem.NewLearningPathRepo())
	env := newTestEnvelope("evt-1", "t1", "g1")
	payload := AtomCreatedPayload{
		AtomID:          "atom-1",
		TenantID:        "t1",
		CourseID:        "c1",
		Title:           "MCQ on agile",
		AtomType:        "mcq",
		Difficulty:      3,
		TopicTags:       []string{"agile", "scrum"},
		CorrectOptionID: "opt-c",
		AnswerCount:     4,
		PublishedAt:     time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, err := atomIdx.Get(context.Background(), "atom-1")
	if err != nil {
		t.Fatalf("Get atom-1: %v", err)
	}
	if got.AtomType != "mcq" {
		t.Errorf("AtomType = %q", got.AtomType)
	}
	if got.CorrectOptionID != "opt-c" {
		t.Errorf("CorrectOptionID = %q; want opt-c", got.CorrectOptionID)
	}
}

// CHO-1968: a freshly atom.created projection MUST be DRAFT (not playable) —
// it only becomes serveable after an atom.published flip. Guards the New()
// status default the subscriber relies on.
func TestAtomCreatedSubscriber_MarksNotPlayable(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, inmem.NewLearningPathRepo())
	env := newTestEnvelope("evt-draft-1", "t1", "g1")
	payload := AtomCreatedPayload{
		AtomID: "atom-draft", TenantID: "t1", AtomType: "mcq", CorrectOptionID: "opt-c",
		AnswerCount: 4, PublishedAt: time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, err := atomIdx.Get(context.Background(), "atom-draft")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != atom_index.StatusDraft {
		t.Errorf("Status = %q; want draft (atom.created must not be playable)", got.Status)
	}
	if got.Playable() {
		t.Errorf("Playable() = true for a freshly-created atom; want false")
	}
}

func TestAtomCreatedSubscriber_RejectsBlankEnvelope(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, inmem.NewLearningPathRepo())
	env := events.Envelope{} // missing mandatory fields
	payload := AtomCreatedPayload{AtomID: "a", TenantID: "t1"}
	if err := sub.Handle(context.Background(), env, payload); err == nil {
		t.Error("expected error on blank envelope")
	}
}

func TestAtomCreatedSubscriber_IdempotentOnDuplicateEventID(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	sub := NewAtomCreatedSubscriber(atomIdx, inmem.NewLearningPathRepo())
	env := newTestEnvelope("evt-1", "t1", "g1")
	payload := AtomCreatedPayload{
		AtomID:      "atom-1",
		TenantID:    "t1",
		AtomType:    "mcq",
		PublishedAt: time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatal(err)
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Subscriber MUST NOT panic on replay; projection state must remain
	// stable.
	got, _ := atomIdx.Get(context.Background(), "atom-1")
	if got == nil || got.AtomID != "atom-1" {
		t.Errorf("replay corrupted state: %v", got)
	}
}

// ---- EnrollmentCreated subscriber ----

func TestEnrollmentCreatedSubscriber_BootstrapsLearningPath(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)

	// Seed atom_index for course c1.
	seedCourseAtoms(t, content, "t1", "c1", "a1", "a2", "a3")

	env := newTestEnvelope("evt-2", "t1", "phyllis")
	env.SourceService = "chora-delivery"
	payload := EnrollmentCreatedPayload{
		EnrollmentID: "enr-1",
		CourseID:     "c1",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		EnrolledAt:   time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v", err)
	}
	if len(p.AtomIDs) != 3 {
		t.Errorf("len AtomIDs = %d; want 3", len(p.AtomIDs))
	}
	if p.CurrentIndex != 0 {
		t.Errorf("CurrentIndex = %d; want 0 on bootstrap", p.CurrentIndex)
	}
	// Subscriber must publish the bootstrapped event.
	if len(publisher.Events()) == 0 {
		t.Error("expected at least one published event")
	}
}

func TestEnrollmentCreatedSubscriber_IdempotentOnDuplicateEnrollment(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)
	seedCourseAtoms(t, content, "t1", "c1", "a1", "a2")
	env := newTestEnvelope("evt-2", "t1", "phyllis")
	env.SourceService = "chora-delivery"
	payload := EnrollmentCreatedPayload{
		EnrollmentID: "enr-1",
		CourseID:     "c1",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		EnrolledAt:   time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatal(err)
	}
	// Second call (network retry / Pub/Sub at-least-once).
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Should still be exactly ONE LearningPath for (t1, c1, phyllis).
	paths, _ := pathRepo.ListByLearner(context.Background(), "t1", "phyllis", 100)
	if len(paths) != 1 {
		t.Errorf("len paths = %d; want 1 (idempotent)", len(paths))
	}
}

// OPEN-1: a content-empty PUBLISHED course must STILL bootstrap a (0-atom)
// path so the enrolled learner is not shown "not enrolled". Atoms are
// appended retroactively as chora.creation.atom.created.v1 events arrive
// (see TestAtomCreatedSubscriber_AppendsAtomToExistingPath). The previous
// behaviour (silently defer + ack) permanently stranded such enrolments.
func TestEnrollmentCreatedSubscriber_BootstrapsEmptyPathWhenCourseHasNoAtoms(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)
	// No atoms seeded.
	env := newTestEnvelope("evt-2", "t1", "phyllis")
	env.SourceService = "chora-delivery"
	payload := EnrollmentCreatedPayload{
		EnrollmentID: "enr-1",
		CourseID:     "empty-course",
		LearnerGCID:  "phyllis",
		TenantID:     "t1",
		EnrolledAt:   time.Now().UTC(),
	}
	if err := sub.Handle(context.Background(), env, payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "empty-course", "phyllis")
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v (expected a 0-atom path, not deferred)", err)
	}
	if len(p.AtomIDs) != 0 {
		t.Errorf("len AtomIDs = %d; want 0", len(p.AtomIDs))
	}
}

// R1: idempotency is anchored on the durable (tenant, course, learner) trio,
// NOT on env.EventID. A publisher re-emitting with a fresh event_id (outbox
// redrive) must NOT create a duplicate path. This also proves the removal of
// the event_id markSeen guard that previously burned the dedupe key BEFORE a
// path was durably created (poison-on-defer).
func TestEnrollmentCreatedSubscriber_DedupesOnTrioAcrossEventIDs(t *testing.T) {
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	sub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)
	seedCourseAtoms(t, content, "t1", "c1", "a1", "a2")
	payload := EnrollmentCreatedPayload{
		EnrollmentID: "enr-1", CourseID: "c1", LearnerGCID: "phyllis",
		TenantID: "t1", EnrolledAt: time.Now().UTC(),
	}
	env1 := newTestEnvelope("evt-AAA", "t1", "phyllis")
	env1.SourceService = "chora-delivery"
	if err := sub.Handle(context.Background(), env1, payload); err != nil {
		t.Fatal(err)
	}
	// Redrive with a DIFFERENT event_id (fresh outbox row).
	env2 := newTestEnvelope("evt-BBB", "t1", "phyllis")
	env2.SourceService = "chora-delivery"
	if err := sub.Handle(context.Background(), env2, payload); err != nil {
		t.Fatalf("redrive: %v", err)
	}
	if paths, _ := pathRepo.ListByLearner(context.Background(), "t1", "phyllis", 100); len(paths) != 1 {
		t.Errorf("len paths = %d; want 1 (trio dedupe across event_ids)", len(paths))
	}
}

// R2: an atom authored AFTER the learner enrolled into a content-empty course
// is appended to the existing path (retroactive fill), so the learner's path
// grows as content lands rather than the atom being lost.
func TestAtomCreatedSubscriber_AppendsAtomToExistingPath(t *testing.T) {
	atomIdx := inmem.NewAtomIndexRepo()
	// An EMPTY curriculum — the learner enrols into a course with no content yet,
	// so the path bootstraps with 0 atoms (the OPEN-1 tolerance).
	content := inmem.NewCourseContentRepo()
	pathRepo := inmem.NewLearningPathRepo()
	publisher := events.NewInMemoryPublisher()
	enrollSub := NewEnrollmentCreatedSubscriber(pathRepo, content, publisher)
	// This still exercises the OTHER append lane, which remains valid: an atom
	// AUTHORED directly against a course (atom.created.v1 carrying a course_id).
	// CHO-2169's point is not that this lane is wrong — it is that it is the only
	// one that existed, and it can never fire for an atom composed into a course
	// through R+. The curriculum lane (course.content_composed.v1) now covers that,
	// and is tested in enrollment_curriculum_test.go.
	atomSub := NewAtomCreatedSubscriber(atomIdx, pathRepo)

	envE := newTestEnvelope("evt-enr", "t1", "phyllis")
	envE.SourceService = "chora-delivery"
	if err := enrollSub.Handle(context.Background(), envE, EnrollmentCreatedPayload{
		EnrollmentID: "e1", CourseID: "c1", LearnerGCID: "phyllis",
		TenantID: "t1", EnrolledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if p, _ := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis"); p == nil || len(p.AtomIDs) != 0 {
		t.Fatalf("precondition: expected 0-atom path; got %+v", p)
	}

	envA := newTestEnvelope("evt-atom", "t1", "author")
	if err := atomSub.Handle(context.Background(), envA, AtomCreatedPayload{
		AtomID: "a1", TenantID: "t1", CourseID: "c1", AtomType: "mcq",
		PublishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	p, err := pathRepo.GetByCourseAndGCID(context.Background(), "t1", "c1", "phyllis")
	if err != nil {
		t.Fatalf("GetByCourseAndGCID: %v", err)
	}
	if len(p.AtomIDs) != 1 || p.AtomIDs[0] != "a1" {
		t.Errorf("AtomIDs = %v; want [a1] (retroactive append)", p.AtomIDs)
	}
}

// ---- helper ----

func seedAtomCreated(t *testing.T, repo *inmem.AtomIndexRepo, atomID, tenantID, courseID string) {
	t.Helper()
	a, err := atomIndexHelper(atomID, tenantID, courseID, "opt-a")
	if err != nil {
		t.Fatalf("seedAtomCreated: %v", err)
	}
	_ = repo.Save(context.Background(), a)
}
